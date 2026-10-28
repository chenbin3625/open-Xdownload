import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import {
  parseDashboardEvent,
  isDashboardJobPayload,
  isDashboardJobList,
  isStaleJobUpdate,
  mergeNewerJobs,
  patchDashboardJobCaches,
  applyDashboardMeta,
  applyDashboardEvent,
  prependJobsToCaches,
  invalidateWorkbenchQueries,
  invalidateAfterReconnect,
  cancelScheduledTerminalRefresh,
  TERMINAL_REFRESH_DEBOUNCE_MS,
  sameJob,
} from "./useDashboardEvents";
import {
  archiveScheduleQueryRoot,
  dashboardMetaQueryRoot,
  failedTweetQueryRoot,
  libraryDownloadsQueryRoot,
  jobsQueryRoot,
  jobFilesQueryRoot,
  type DashboardMeta,
  type Job,
  type JobsPage,
} from "./api";

function makeJob(overrides: Partial<Job> = {}): Job {
  return {
    id: 1,
    kind: "tweet_link",
    status: "downloading",
    input: "https://x.com/u/status/1",
    title: "t",
    progress: 0.5,
    message: "正在下载",
    error: undefined,
    createdAt: "2024-01-01T00:00:00Z",
    updatedAt: "2024-01-01T00:00:00Z",
    ...overrides,
  };
}

function makeJobsPage(jobs: Job[]): JobsPage {
  return { items: jobs, page: 1, pageSize: 20 };
}

function makeMeta(jobs: Job[]): DashboardMeta {
  return {
    failedTweetCount: 0,
    stats: { total: jobs.length, active: jobs.length, completed: 0, failed: 0 },
  };
}

it("refreshes open job files after an SSE reconnect", async () => {
  const queryClient = new QueryClient();
  queryClient.setQueryData([...jobFilesQueryRoot, 1], { downloads: [], failed: [] });

  await invalidateAfterReconnect(queryClient);

  expect(isInvalidated(queryClient, [...jobFilesQueryRoot, 1])).toBe(true);
});

describe("applyDashboardMeta", () => {
  it("replaces meta without touching the jobs page cache", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const jobs = makeJobsPage([makeJob()]);
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], jobs);
    applyDashboardMeta(queryClient, {
      stats: { total: 9, active: 1, completed: 8, failed: 0 },
      failedTweetCount: 4,
    });
    expect(queryClient.getQueryData([...jobsQueryRoot, 1, 20])).toBe(jobs);
    expect(queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot)?.failedTweetCount).toBe(4);
  });
});
describe("parseDashboardEvent", () => {
  it("parses valid JSON and rejects garbage", () => {
    expect(parseDashboardEvent('{"type":"job.updated","jobId":1}')).toEqual({
      type: "job.updated",
      jobId: 1,
    });
    expect(parseDashboardEvent("not json")).toBeNull();
    expect(parseDashboardEvent("42")).toBeNull();
  });
});

describe("isDashboardJobPayload", () => {
  it("recognizes a job payload", () => {
    expect(isDashboardJobPayload(makeJob())).toBe(true);
    expect(isDashboardJobPayload({})).toBe(false);
    expect(isDashboardJobPayload(null)).toBe(false);
    expect(isDashboardJobPayload("x")).toBe(false);
  });

  it("rejects payloads without a usable createdAt", () => {
    const { createdAt: _createdAt, ...withoutCreatedAt } = makeJob();
    expect(isDashboardJobPayload(withoutCreatedAt)).toBe(false);
    expect(isDashboardJobPayload({ ...makeJob(), createdAt: 123 })).toBe(false);
  });
});

describe("sameJob", () => {
  it("is true for identical jobs and false on any delta", () => {
    const a = makeJob();
    expect(sameJob(a, makeJob())).toBe(true);
    expect(sameJob(a, makeJob({ progress: 0.6 }))).toBe(false);
    expect(sameJob(a, makeJob({ message: "x" }))).toBe(false);
    expect(sameJob(a, makeJob({ status: "completed" }))).toBe(false);
  });
});

describe("patchDashboardJobCaches", () => {
  it("patches the matching job in place and updates stats", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const j1 = makeJob({ id: 1, status: "downloading" });
    const j2 = makeJob({ id: 2, status: "pending" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([j1, j2]));
    queryClient.setQueryData(dashboardMetaQueryRoot, makeMeta([j1, j2]));

    const updated = makeJob({ id: 1, status: "completed", progress: 1 });
    const result = patchDashboardJobCaches(queryClient, updated);

    expect(result.found).toBe(true);
    expect(result.needsFullRefresh).toBe(true);
    const page = queryClient.getQueryData<JobsPage>([...jobsQueryRoot, 1, 20]);
    expect(page?.items.find((job) => job.id === 1)?.status).toBe("completed");
    const meta = queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot);
    expect(meta?.stats.active).toBe(1);
    expect(meta?.stats.completed).toBe(1);
  });

  it("reports not found when the job is absent", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([makeJob({ id: 9 })]));
    const result = patchDashboardJobCaches(queryClient, makeJob({ id: 10 }));
    expect(result.found).toBe(false);
  });
});

describe("prependJobsToCaches", () => {
  it("inserts new jobs on page 1 and bumps stats once", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const existing = makeJob({ id: 1, status: "pending" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([existing]));
    queryClient.setQueryData(dashboardMetaQueryRoot, makeMeta([existing]));

    const created = makeJob({ id: 2, status: "pending" });
    prependJobsToCaches(queryClient, [created]);
    prependJobsToCaches(queryClient, [created]);

    const page = queryClient.getQueryData<JobsPage>([...jobsQueryRoot, 1, 20]);
    expect(page?.items.map((job) => job.id)).toEqual([2, 1]);
    const meta = queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot);
    expect(meta?.stats.total).toBe(2);
    expect(meta?.stats.active).toBe(2);
  });

  it("does not insert into page 2 but still counts unseen jobs", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const existing = makeJob({ id: 9, status: "pending" });
    queryClient.setQueryData([...jobsQueryRoot, 2, 20], { items: [existing], page: 2, pageSize: 20 });
    queryClient.setQueryData(dashboardMetaQueryRoot, makeMeta([existing]));

    prependJobsToCaches(queryClient, [makeJob({ id: 10, status: "pending" })]);
    const page = queryClient.getQueryData<JobsPage>([...jobsQueryRoot, 2, 20]);
    expect(page?.items.map((job) => job.id)).toEqual([9]);
    expect(queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot)?.stats.total).toBe(2);
  });
});

describe("applyDashboardEvent", () => {
  it("invalidates the failed-tweet queue when a retry job finishes", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData([...failedTweetQueryRoot, 1, 10], { items: [{ id: 1 }] });

    applyDashboardEvent(queryClient, {
      type: "job.updated",
      payload: makeJob({ kind: "failed_retry", status: "completed" }),
    });

    expect(isInvalidated(queryClient, [...failedTweetQueryRoot, 1, 10])).toBe(true);
  });
  it("uses server meta after batch create instead of double-counting", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const existing = makeJob({ id: 1, status: "pending" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([existing]));
    queryClient.setQueryData(dashboardMetaQueryRoot, makeMeta([existing]));

    const created = [makeJob({ id: 2, status: "pending" }), makeJob({ id: 3, status: "pending" })];
    const result = applyDashboardEvent(queryClient, {
      type: "jobs.created",
      payload: created,
      meta: { stats: { total: 3, active: 3, completed: 0, failed: 0 }, failedTweetCount: 0 },
    });

    expect(result).toBe("handled");
    expect(isDashboardJobList(created)).toBe(true);
    const page = queryClient.getQueryData<JobsPage>([...jobsQueryRoot, 1, 20]);
    expect(page?.items.map((job) => job.id)).toEqual([2, 3, 1]);
    const meta = queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot);
    expect(meta?.stats).toEqual({ total: 3, active: 3, completed: 0, failed: 0 });
  });

  it("skips meta refetch when a terminal update carries stats", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const job = makeJob({ id: 1, status: "downloading" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([job]));
    queryClient.setQueryData(dashboardMetaQueryRoot, makeMeta([job]));

    const result = applyDashboardEvent(queryClient, {
      type: "job.updated",
      payload: makeJob({ id: 1, status: "completed", progress: 1 }),
      meta: { stats: { total: 1, active: 0, completed: 1, failed: 0 }, failedTweetCount: 4 },
    });

    expect(result).toBe("handled");
    expect(queryClient.getQueryData<DashboardMeta>(dashboardMetaQueryRoot)?.failedTweetCount).toBe(4);
  });
});

describe("isStaleJobUpdate", () => {
  it("flags older updatedAt and tolerates unparsable timestamps", () => {
    const previous = makeJob({ updatedAt: "2024-01-01T00:00:10Z" });
    expect(isStaleJobUpdate(previous, makeJob({ updatedAt: "2024-01-01T00:00:05Z" }))).toBe(true);
    expect(isStaleJobUpdate(previous, makeJob({ updatedAt: "2024-01-01T00:00:20Z" }))).toBe(false);
    expect(isStaleJobUpdate(previous, makeJob({ updatedAt: "2024-01-01T00:00:10Z" }))).toBe(false);
    expect(isStaleJobUpdate(previous, makeJob({ updatedAt: "not a date" }))).toBe(false);
  });
});

describe("mergeNewerJobs", () => {
  it("does not restore a recently updated job pushed off the first page", () => {
    const now = Date.now();
    const jobs = [1, 2, 3].map((id) => makeJob({
      id,
      createdAt: new Date(now - (4 - id) * 1000).toISOString(),
      updatedAt: new Date(now).toISOString(),
    }));
    const cached = { items: [jobs[1], jobs[0]], page: 1, pageSize: 2 };
    const fetched = { items: [jobs[2], jobs[1]], page: 1, pageSize: 2 };

    expect(mergeNewerJobs(cached, fetched).items.map((job) => job.id)).toEqual([3, 2]);
  });

  it("retains newly created SSE jobs in server order within the page size", () => {
    const timestamp = new Date().toISOString();
    const jobs = [1, 2, 3, 4].map((id) => makeJob({ id, createdAt: timestamp, updatedAt: timestamp }));
    const cached = { items: [jobs[2], jobs[3]], page: 1, pageSize: 2 };
    const fetched = { items: [jobs[1], jobs[0]], page: 1, pageSize: 2 };

    expect(mergeNewerJobs(cached, fetched).items.map((job) => job.id)).toEqual([4, 3]);
  });

  it("does not retain a cached row that sorts below the fetched newest row", () => {
    const old = makeJob({ id: 1, createdAt: "2024-01-01T00:00:00Z", updatedAt: "2024-05-01T00:00:00Z" });
    const fresh = makeJob({ id: 2, createdAt: "2024-06-01T00:00:00Z", updatedAt: "2024-06-01T00:00:00Z" });
    const fetched = makeJobsPage([fresh]);

    expect(mergeNewerJobs(makeJobsPage([old]), fetched).items.map((job) => job.id)).toEqual([2]);
  });

  it("retains SSE rows without consulting the browser clock", () => {
    // 保留判定只看"是否比 fetched 首行更新"，与 Date.now() 无关：
    // 即使这行创建于 10 分钟前（远超旧的 30s 窗口），只要 fetched 快照更旧就必须保留。
    const staleSnapshotTop = makeJob({ id: 1, createdAt: "2024-01-01T00:00:00Z" });
    const sseInserted = makeJob({ id: 2, createdAt: "2024-01-01T00:10:00Z" });
    const fetched = makeJobsPage([staleSnapshotTop]);

    expect(mergeNewerJobs(makeJobsPage([sseInserted]), fetched).items.map((job) => job.id)).toEqual([2, 1]);
  });

  it("drops a cached row whose createdAt cannot be parsed", () => {
    const broken = makeJob({ id: 9, createdAt: "not a date", updatedAt: "not a date" });
    const fetched = makeJobsPage([
      makeJob({ id: 1, createdAt: "2024-01-01T00:00:00Z" }),
    ]);

    expect(mergeNewerJobs(makeJobsPage([broken]), fetched).items.map((job) => job.id)).toEqual([1]);
  });

  it("retains a newly created SSE job while an empty first page is in flight", () => {
    const timestamp = new Date().toISOString();
    const job = makeJob({ createdAt: timestamp, updatedAt: timestamp });

    expect(mergeNewerJobs(makeJobsPage([job]), makeJobsPage([])).items).toEqual([job]);
  });

  it("keeps the cached job when the fetched page is older", () => {
    const cached = makeJobsPage([
      makeJob({ id: 1, progress: 0.9, updatedAt: "2024-01-01T00:00:10Z" }),
    ]);
    const fetched = makeJobsPage([
      makeJob({ id: 1, progress: 0.2, updatedAt: "2024-01-01T00:00:05Z" }),
    ]);
    expect(mergeNewerJobs(cached, fetched).items[0].progress).toBe(0.9);
  });

  it("takes the fetched job when it is newer and passes through without cache", () => {
    const cached = makeJobsPage([
      makeJob({ id: 1, progress: 0.2, updatedAt: "2024-01-01T00:00:05Z" }),
    ]);
    const fetched = makeJobsPage([
      makeJob({ id: 1, progress: 0.9, updatedAt: "2024-01-01T00:00:10Z" }),
    ]);
    expect(mergeNewerJobs(cached, fetched).items[0].progress).toBe(0.9);
    expect(mergeNewerJobs(undefined, fetched)).toBe(fetched);
  });
});

describe("patchDashboardJobCaches ordering guard", () => {
  it("refuses to overwrite a newer cached job with a late event", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const fresh = makeJob({ id: 1, progress: 0.9, updatedAt: "2024-01-01T00:00:10Z" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([fresh]));
    queryClient.setQueryData(dashboardMetaQueryRoot, makeMeta([fresh]));

    const late = makeJob({ id: 1, progress: 0.2, updatedAt: "2024-01-01T00:00:05Z" });
    const result = patchDashboardJobCaches(queryClient, late);

    expect(result.stale).toBe(true);
    expect(result.found).toBe(false);
    const page = queryClient.getQueryData<JobsPage>([...jobsQueryRoot, 1, 20]);
    expect(page?.items[0].progress).toBe(0.9);
  });

  it("discards a late event instead of driving a stats refresh", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const fresh = makeJob({ id: 1, status: "completed", updatedAt: "2024-01-01T00:00:10Z" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([fresh]));

    const result = applyDashboardEvent(queryClient, {
      type: "job.updated",
      payload: makeJob({ id: 1, status: "downloading", updatedAt: "2024-01-01T00:00:05Z" }),
    });

    expect(result).toBe("handled");
    expect(queryClient.getQueryData<JobsPage>([...jobsQueryRoot, 1, 20])?.items[0].status).toBe("completed");
  });

  it("asks for a stats refresh when a non-terminal update crosses buckets", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const done = makeJob({ id: 1, status: "completed", updatedAt: "2024-01-01T00:00:05Z" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([done]));
    queryClient.setQueryData(dashboardMetaQueryRoot, makeMeta([done]));

    const result = applyDashboardEvent(queryClient, {
      type: "job.updated",
      payload: makeJob({ id: 1, status: "downloading", updatedAt: "2024-01-01T00:00:10Z" }),
    });

    expect(result).toBe("refresh-meta");
  });
});

function isInvalidated(queryClient: QueryClient, key: readonly unknown[]) {
  return queryClient.getQueryState(key)?.isInvalidated ?? false;
}

function seedRoots(queryClient: QueryClient) {
  queryClient.setQueryData(archiveScheduleQueryRoot, []);
  queryClient.setQueryData(libraryDownloadsQueryRoot, { items: [] });
  queryClient.setQueryData(jobsQueryRoot, makeJobsPage([]));
}

describe("archive schedule events", () => {
  it.each(["archive_schedule.created", "archive_schedule.updated", "archive_schedule.deleted", "archive_schedule.ran"])(
    "%s only invalidates the schedule query",
    (type) => {
      const queryClient = new QueryClient();
      seedRoots(queryClient);
      const result = applyDashboardEvent(queryClient, { type, payload: { id: 1 } });
      expect(result).toBe("handled");
      expect(isInvalidated(queryClient, archiveScheduleQueryRoot)).toBe(true);
      expect(isInvalidated(queryClient, libraryDownloadsQueryRoot)).toBe(false);
      expect(isInvalidated(queryClient, jobsQueryRoot)).toBe(false);
    },
  );

  it("invalidateWorkbenchQueries also refreshes the schedule list", async () => {
    const queryClient = new QueryClient();
    seedRoots(queryClient);
    await invalidateWorkbenchQueries(queryClient);
    expect(isInvalidated(queryClient, archiveScheduleQueryRoot)).toBe(true);
  });
});

describe("terminal job.updated", () => {
  it("invalidates the library downloads query", () => {
    const queryClient = new QueryClient();
    const job = makeJob({ id: 1, status: "downloading" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([job]));
    queryClient.setQueryData([...libraryDownloadsQueryRoot, "all"], { items: [] });
    applyDashboardEvent(queryClient, {
      type: "job.updated",
      payload: makeJob({ id: 1, status: "completed", progress: 1, updatedAt: "2024-01-01T00:00:10Z" }),
      meta: { stats: { total: 1, active: 0, completed: 1, failed: 0 }, failedTweetCount: 0 },
    });
    expect(isInvalidated(queryClient, [...libraryDownloadsQueryRoot, "all"])).toBe(true);
  });

  it("leaves the library query alone for non-terminal progress", () => {
    const queryClient = new QueryClient();
    const job = makeJob({ id: 1, status: "downloading" });
    queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([job]));
    queryClient.setQueryData(libraryDownloadsQueryRoot, { items: [] });
    applyDashboardEvent(queryClient, {
      type: "job.updated",
      payload: makeJob({ id: 1, progress: 0.8, updatedAt: "2024-01-01T00:00:10Z" }),
    });
    expect(isInvalidated(queryClient, libraryDownloadsQueryRoot)).toBe(false);
    cancelScheduledTerminalRefresh(queryClient);
  });

  it("coalesces a burst of terminal updates into a single refetch", async () => {
    vi.useFakeTimers();
    try {
      const queryClient = new QueryClient();
      const job = makeJob({ id: 1, status: "downloading" });
      queryClient.setQueryData([...jobsQueryRoot, 1, 20], makeJobsPage([job]));
      queryClient.setQueryData([...libraryDownloadsQueryRoot, "all"], { items: [] });
      queryClient.setQueryData([...failedTweetQueryRoot, 1, 10], { items: [] });
      const refetchSpy = vi.spyOn(queryClient, "refetchQueries");

      for (let index = 0; index < 5; index += 1) {
        applyDashboardEvent(queryClient, {
          type: "job.updated",
          payload: makeJob({ id: 1, status: "completed", progress: 1, updatedAt: "2024-01-01T00:00:10Z" }),
        });
      }

      // invalidateQueries(refetchType:"none") 内部也会调用 refetchQueries，但 type 是 none；
      // 真正会发网络请求的是 type:"active" 的那一次。
      // 终态事件还会对已打开的任务详情（job-files）发一次失效，这里只统计媒体库/失败队列。
      const activeRefetches = () =>
        refetchSpy.mock.calls.filter(([filters]) => {
          const next = filters as { type?: string; queryKey?: readonly unknown[] };
          if (next?.type !== "active") return false;
          const key = JSON.stringify(next.queryKey ?? []);
          return key.includes("library-downloads") || key.includes("failed-tweets");
        });

      // 失效标记是同步生效的（不发请求），合并刷新要等去抖窗口结束。
      expect(isInvalidated(queryClient, [...libraryDownloadsQueryRoot, "all"])).toBe(true);
      expect(isInvalidated(queryClient, [...failedTweetQueryRoot, 1, 10])).toBe(true);
      expect(activeRefetches()).toHaveLength(0);

      await vi.advanceTimersByTimeAsync(TERMINAL_REFRESH_DEBOUNCE_MS);
      expect(activeRefetches()).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
