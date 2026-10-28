import { useCallback, useEffect, useRef, useState } from "react";
import type { QueryClient } from "@tanstack/react-query";
import type { DashboardMeta, Job, JobsPage } from "./api";
import {
  archiveScheduleQueryRoot,
  dashboardMetaQueryRoot,
  failedTweetQueryRoot,
  getDashboardMeta,
  jobFilesQueryRoot,
  libraryDownloadsQueryRoot,
  jobsQueryRoot,
} from "./api";
import { isDashboardMeta } from "./bootstrap";
import {
  dashboardStatsWithJobStatusChange,
  isJobTerminal,
  jobStatusBucket,
} from "./jobStatus";

export function invalidateWorkbenchQueries(queryClient: QueryClient) {
  // 整体刷新会立即重拉媒体库，挂起的"终态合并刷新"就没必要再发一次了。
  cancelScheduledTerminalRefresh(queryClient);
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: jobsQueryRoot }),
    queryClient.invalidateQueries({ queryKey: dashboardMetaQueryRoot }),
    queryClient.invalidateQueries({ queryKey: libraryDownloadsQueryRoot }),
    queryClient.invalidateQueries({ queryKey: archiveScheduleQueryRoot }),
  ]);
}

// 后端 SSE 不支持断点续传，断线期间的事件全部丢失；重连成功后整体补拉一次。
export function invalidateAfterReconnect(queryClient: QueryClient) {
  return Promise.all([
    invalidateWorkbenchQueries(queryClient),
    queryClient.invalidateQueries({ queryKey: failedTweetQueryRoot }),
    queryClient.invalidateQueries({ queryKey: jobFilesQueryRoot }),
  ]);
}

export type DashboardEvent = {
  type?: string;
  jobId?: number;
  payload?: unknown;
  meta?: DashboardMeta;
  timestamp?: string;
};

export function parseDashboardEvent(raw: string): DashboardEvent | null {
  try {
    const parsed = JSON.parse(raw) as DashboardEvent;
    return parsed && typeof parsed === "object" ? parsed : null;
  } catch {
    return null;
  }
}

export function isDashboardJobPayload(payload: unknown): payload is Job {
  if (!payload || typeof payload !== "object") return false;
  const job = payload as Partial<Job>;
  return (
    typeof job.id === "number" &&
    typeof job.status === "string" &&
    // createdAt 参与分页合并排序，缺失/非字符串的 payload 一律拒绝（后续走整页刷新兜底）。
    typeof job.createdAt === "string"
  );
}

export function isDashboardJobList(payload: unknown): payload is Job[] {
  return Array.isArray(payload) && payload.every(isDashboardJobPayload);
}

export function sameJob(left: Job, right: Job) {
  return (
    left.id === right.id &&
    left.kind === right.kind &&
    left.status === right.status &&
    left.input === right.input &&
    left.title === right.title &&
    left.progress === right.progress &&
    left.message === right.message &&
    left.error === right.error &&
    left.createdAt === right.createdAt &&
    left.updatedAt === right.updatedAt
  );
}

// 事件与分页响应都可能迟到，updatedAt 更旧的一份不能覆盖已经写入的新状态，
// 否则进度条会肉眼可见地往回跳。时间戳缺失/不可解析时不做拦截，避免误伤。
export function isStaleJobUpdate(previous: Job, next: Job) {
  const previousTime = Date.parse(previous.updatedAt);
  const nextTime = Date.parse(next.updatedAt);
  if (Number.isNaN(previousTime) || Number.isNaN(nextTime)) return false;
  return nextTime < previousTime;
}

// 800ms 去抖刷新可能在更新的 SSE 补丁之后才落地：逐条比较 updatedAt，保留较新的一份。
// 同时保留 SSE 在 fetch 期间新插入的任务：只要它比 fetched 首行"更新"，按后端
// ORDER BY created_at DESC, id DESC 的排序语义它本该出现在第 1 页首位。
// 这里刻意不比较浏览器时钟（客户端时钟与服务端可能差几十秒），只做服务器排序语义比较；
// createdAt 非法时直接不保留，避免 NaN 比较把整页排序打乱。
export function mergeNewerJobs(cached: JobsPage | undefined, fetched: JobsPage): JobsPage {
  if (!cached) return fetched;
  let changed = false;
  const items = fetched.items.map((job) => {
    const previous = cached.items.find((item) => item.id === job.id);
    if (previous && isStaleJobUpdate(previous, job)) {
      changed = true;
      return previous;
    }
    return job;
  });

  // 保留由 SSE 插入但尚未被轮询覆盖的任务（仅第一页）。
  if (fetched.page === 1) {
    const fetchedIds = new Set(fetched.items.map((job) => job.id));
    const newestFetched = fetched.items[0];
    const newestCreatedTime = newestFetched ? Date.parse(newestFetched.createdAt) : Number.NaN;
    // fetched 首页为空说明快照里没有任何任务：缓存里不属于该快照的行一定是快照之后插入的。
    const fetchedIsEmpty = fetched.items.length === 0;
    const retained = cached.items.filter((job) => {
      if (fetchedIds.has(job.id)) return false;
      const createdTime = Date.parse(job.createdAt);
      if (!Number.isFinite(createdTime)) return false;
      if (fetchedIsEmpty) return true;
      if (!Number.isFinite(newestCreatedTime)) return false;
      return (
        createdTime > newestCreatedTime ||
        (createdTime === newestCreatedTime && job.id > newestFetched.id)
      );
    });
    if (retained.length > 0) {
      changed = true;
      items.unshift(...retained);
      items.sort(
        (left, right) =>
          Date.parse(right.createdAt) - Date.parse(left.createdAt) || right.id - left.id,
      );
      items.splice(fetched.pageSize);
    }
  }

  return changed ? { ...fetched, items } : fetched;
}

// 一次批量归档收尾会连续发出上百条终态 job.updated；逐条 invalidate 会产生同样数量的
// 媒体库/失败队列请求。这里改成"立即标记失效（refetchType:none，不发请求）+ 300ms 合并后
// 统一 refetch"，既不丢失效语义（期间新挂载的查询仍会自己拉），也把请求压到一次。
export const TERMINAL_REFRESH_DEBOUNCE_MS = 300;

type TerminalRefreshTimers = { timer: ReturnType<typeof setTimeout> | null };
const terminalRefreshTimers = new WeakMap<QueryClient, TerminalRefreshTimers>();

function terminalTimersFor(queryClient: QueryClient): TerminalRefreshTimers {
  let timers = terminalRefreshTimers.get(queryClient);
  if (!timers) {
    timers = { timer: null };
    terminalRefreshTimers.set(queryClient, timers);
  }
  return timers;
}

export function scheduleTerminalDataRefresh(queryClient: QueryClient) {
  void queryClient.invalidateQueries({ queryKey: libraryDownloadsQueryRoot, refetchType: "none" });
  void queryClient.invalidateQueries({ queryKey: failedTweetQueryRoot, refetchType: "none" });
  const timers = terminalTimersFor(queryClient);
  if (timers.timer) clearTimeout(timers.timer);
  timers.timer = setTimeout(() => {
    timers.timer = null;
    void queryClient.refetchQueries({ queryKey: libraryDownloadsQueryRoot, type: "active" });
    void queryClient.refetchQueries({ queryKey: failedTweetQueryRoot, type: "active" });
  }, TERMINAL_REFRESH_DEBOUNCE_MS);
}

/** 取消挂起的终态失效刷新（手动刷新/卸载时调用，避免多余的一次合并请求）。 */
export function cancelScheduledTerminalRefresh(queryClient: QueryClient) {
  const timers = terminalRefreshTimers.get(queryClient);
  if (timers?.timer) {
    clearTimeout(timers.timer);
    timers.timer = null;
  }
}

export function patchDashboardJobCaches(queryClient: QueryClient, updatedJob: Job) {
  let found = false;
  let needsFullRefresh = false;
  let stale = false;
  let previousStatus: Job["status"] | undefined;

  queryClient.setQueriesData<JobsPage>({ queryKey: jobsQueryRoot }, (current) => {
    if (!current) return current;

    const jobIndex = current.items.findIndex((job) => job.id === updatedJob.id);
    if (jobIndex === -1) return current;

    const previousJob = current.items[jobIndex];
    if (isStaleJobUpdate(previousJob, updatedJob)) {
      stale = true;
      return current;
    }

    found = true;
    previousStatus = previousJob.status;
    if (
      jobStatusBucket(previousJob.status) !== jobStatusBucket(updatedJob.status) ||
      isJobTerminal(updatedJob.status)
    ) {
      needsFullRefresh = true;
    }
    if (sameJob(previousJob, updatedJob)) return current;

    const items = [...current.items];
    items[jobIndex] = updatedJob;
    return { ...current, items };
  });

  if (found && previousStatus) {
    const fromStatus = previousStatus;
    queryClient.setQueryData<DashboardMeta>(dashboardMetaQueryRoot, (current) => {
      if (!current) return current;
      const stats = dashboardStatsWithJobStatusChange(current.stats, fromStatus, updatedJob.status);
      if (stats === current.stats) return current;
      return { ...current, stats };
    });
  }

  return { found, needsFullRefresh, stale };
}

export function prependJobsToCaches(queryClient: QueryClient, jobs: Job[]) {
  if (jobs.length === 0) return;

  const known = new Set<number>();
  for (const [, page] of queryClient.getQueriesData<JobsPage>({ queryKey: jobsQueryRoot })) {
    for (const job of page?.items ?? []) {
      known.add(job.id);
    }
  }
  const fresh = jobs.filter((job) => !known.has(job.id));
  if (fresh.length === 0) return;

  queryClient.setQueriesData<JobsPage>({ queryKey: jobsQueryRoot }, (current) => {
    if (!current || current.page !== 1) return current;
    return { ...current, items: [...fresh, ...current.items].slice(0, current.pageSize) };
  });

  queryClient.setQueryData<DashboardMeta>(dashboardMetaQueryRoot, (current) => {
    if (!current) return current;
    return {
      ...current,
      stats: {
        ...current.stats,
        total: current.stats.total + fresh.length,
        active: current.stats.active + fresh.length,
      },
    };
  });
}

export function applyDashboardMeta(queryClient: QueryClient, meta: DashboardMeta) {
  queryClient.setQueryData(dashboardMetaQueryRoot, meta);
}

export type ApplyDashboardEventResult =
  | "handled"
  | "refresh-workbench"
  | "refresh-meta"
  | "refresh-files-meta";

export function applyDashboardEvent(
  queryClient: QueryClient,
  event: DashboardEvent,
): ApplyDashboardEventResult {
  const applyEventMeta = () => {
    if (isDashboardMeta(event.meta)) {
      applyDashboardMeta(queryClient, event.meta);
      return true;
    }
    return false;
  };
  // created/updated/deleted/ran 只影响计划列表；ran 产生的任务另有 jobs.created 事件。
  if (event.type?.startsWith("archive_schedule.")) {
    void queryClient.invalidateQueries({ queryKey: archiveScheduleQueryRoot });
    return "handled";
  }
  if (event.type === "failed_tweet.deleted" || event.type === "failed_tweets.cleared") {
    void queryClient.invalidateQueries({ queryKey: failedTweetQueryRoot });
    return applyEventMeta() ? "handled" : "refresh-meta";
  }
  if (event.type === "jobs.created" && isDashboardJobList(event.payload)) {
    prependJobsToCaches(queryClient, event.payload);
    // 新任务一定是 active 的，但 meta 缓存可能尚不知道；确保刷新统计。
    if (!applyEventMeta()) return "refresh-meta";
    return "handled";
  }
  if (event.type === "job.created" && isDashboardJobPayload(event.payload)) {
    prependJobsToCaches(queryClient, [event.payload]);
    if (!applyEventMeta()) return "refresh-meta";
    return "handled";
  }
  if (event.type === "job.updated" && isDashboardJobPayload(event.payload)) {
    const patched = patchDashboardJobCaches(queryClient, event.payload);
    // 迟到的事件已被丢弃，不能再拿它去驱动统计刷新。
    if (patched.stale) return "handled";
    // 任务结束意味着可能落了新文件；媒体库 staleTime 很长，需主动失效（只会重拉活跃查询）。
    // 批量收尾时合并成一次刷新，避免 200 个终态事件打出 200 次媒体库请求。
    if (isJobTerminal(event.payload.status)) {
      scheduleTerminalDataRefresh(queryClient);
    }
    if (patched.found) {
      if (isJobTerminal(event.payload.status)) {
        void queryClient.invalidateQueries({ queryKey: [...jobFilesQueryRoot, event.payload.id] });
        return applyEventMeta() ? "handled" : "refresh-files-meta";
      }
      // 状态跨桶迁移（如 active → failed）会改变统计分布，必须补一次统计刷新。
      if (patched.needsFullRefresh) {
        return applyEventMeta() ? "handled" : "refresh-meta";
      }
      return "handled";
    }
    return applyEventMeta() ? "handled" : "refresh-meta";
  }
  return "refresh-workbench";
}

// EventSource 只在网络中断时自动重连；服务端返回非 200（订阅数达上限时后端回 503）
// 会让连接永久停在 CLOSED，界面从此不再更新。这里显式重建，退避上限 30s。
const sseRetryBaseDelay = 3_000;
const sseRetryMaxDelay = 30_000;

export function useDashboardEvents(queryClient: QueryClient, onRefresh: () => void, enabled = true) {
  const [sseConnected, setSseConnected] = useState(true);
  const [epoch, setEpoch] = useState(0);
  const retryDelayRef = useRef(sseRetryBaseDelay);
  const reconnectPendingRef = useRef(false);

  useEffect(() => {
    if (!enabled) {
      setSseConnected(true);
      retryDelayRef.current = sseRetryBaseDelay;
      return;
    }
    const events = new EventSource("/api/events");
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let metaTimer: ReturnType<typeof setTimeout> | null = null;
    let metaController: AbortController | null = null;
    let disposed = false;

    const scheduleRefresh = () => {
      if (disposed || timer) return;
      timer = setTimeout(() => {
        timer = null;
        onRefresh();
      }, 800);
    };

    const scheduleMetaRefresh = () => {
      if (disposed || metaTimer || metaController) return;
      metaTimer = setTimeout(() => {
        metaTimer = null;
        const controller = new AbortController();
        metaController = controller;
        void getDashboardMeta(controller.signal)
          .then((meta) => { if (!disposed) applyDashboardMeta(queryClient, meta); })
          .catch((error: unknown) => {
            if (!(error instanceof Error && error.name === "AbortError")) scheduleRefresh();
          })
          .finally(() => {
            if (metaController === controller) metaController = null;
          });
      }, 800);
    };

    events.onopen = () => {
      setSseConnected(true);
      retryDelayRef.current = sseRetryBaseDelay;
      if (reconnectPendingRef.current) {
        reconnectPendingRef.current = false;
        void invalidateAfterReconnect(queryClient);
      }
    };

    events.onerror = () => {
      setSseConnected(false);
      // 标记放在 ref 上：CLOSED 后会换新的 EventSource 实例，标记需要跨实例保留。
      reconnectPendingRef.current = true;
      scheduleRefresh();
      // CLOSED 表示 EventSource 已放弃重试，只能换一个实例重新连。
      if (events.readyState === EventSource.CLOSED && !disposed && !retryTimer) {
        const delay = retryDelayRef.current;
        retryDelayRef.current = Math.min(delay * 2, sseRetryMaxDelay);
        retryTimer = setTimeout(() => {
          retryTimer = null;
          if (!disposed) setEpoch((current) => current + 1);
        }, delay);
      }
    };

    events.onmessage = (message) => {
      const event = parseDashboardEvent(message.data);
      if (!event) {
        scheduleRefresh();
        return;
      }
      switch (applyDashboardEvent(queryClient, event)) {
        case "refresh-workbench":
          scheduleRefresh();
          return;
        case "refresh-meta":
          scheduleMetaRefresh();
          return;
        case "refresh-files-meta":
          scheduleMetaRefresh();
          return;
        default:
          return;
      }
    };

    return () => {
      disposed = true;
      events.close();
      if (timer) clearTimeout(timer);
      if (metaTimer) clearTimeout(metaTimer);
      if (retryTimer) clearTimeout(retryTimer);
      metaController?.abort();
      cancelScheduledTerminalRefresh(queryClient);
    };
  }, [enabled, epoch, queryClient, onRefresh]);

  // 用户点"重连"时丢弃当前 EventSource 并立即重建（epoch 变化会触发 effect 重跑）。
  const reconnect = useCallback(() => {
    reconnectPendingRef.current = true;
    setSseConnected(false);
    retryDelayRef.current = sseRetryBaseDelay;
    setEpoch((current) => current + 1);
  }, []);

  return { sseConnected, reconnect };
}
