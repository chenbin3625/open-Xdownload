import { useEffect, useState } from "react";
import type { QueryClient } from "@tanstack/react-query";
import type { DashboardMeta, Job, JobsPage } from "./api";
import {
  archiveScheduleQueryRoot,
  dashboardMetaQueryRoot,
  failedTweetQueryRoot,
  getDashboardMeta,
  jobFilesQueryRoot,
  jobsQueryRoot,
} from "./api";
import { isDashboardMeta } from "./bootstrap";
import {
  dashboardStatsWithJobStatusChange,
  isJobTerminal,
  jobStatusBucket,
} from "./jobStatus";

export function invalidateWorkbenchQueries(queryClient: QueryClient) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: jobsQueryRoot }),
    queryClient.invalidateQueries({ queryKey: dashboardMetaQueryRoot }),
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
  return typeof job.id === "number" && typeof job.status === "string";
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

export function patchDashboardJobCaches(queryClient: QueryClient, updatedJob: Job) {
  let found = false;
  let needsFullRefresh = false;
  let previousStatus: Job["status"] | undefined;

  queryClient.setQueriesData<JobsPage>({ queryKey: jobsQueryRoot }, (current) => {
    if (!current) return current;

    const jobIndex = current.items.findIndex((job) => job.id === updatedJob.id);
    if (jobIndex === -1) return current;

    found = true;
    const previousJob = current.items[jobIndex];
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

  return { found, needsFullRefresh };
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
  if (event.type === "archive_schedule.updated" || event.type === "archive_schedule.created") {
    queryClient.invalidateQueries({ queryKey: archiveScheduleQueryRoot });
    return "handled";
  }
  if (event.type === "archive_schedule.ran") {
    queryClient.invalidateQueries({ queryKey: archiveScheduleQueryRoot });
    return "handled";
  }
  if (event.type === "failed_tweet.deleted" || event.type === "failed_tweets.cleared") {
    queryClient.invalidateQueries({ queryKey: failedTweetQueryRoot });
    return applyEventMeta() ? "handled" : "refresh-meta";
  }
  if (event.type === "jobs.created" && isDashboardJobList(event.payload)) {
    prependJobsToCaches(queryClient, event.payload);
    applyEventMeta();
    return "handled";
  }
  if (event.type === "job.created" && isDashboardJobPayload(event.payload)) {
    prependJobsToCaches(queryClient, [event.payload]);
    applyEventMeta();
    return "handled";
  }
  if (event.type === "job.updated" && isDashboardJobPayload(event.payload)) {
    const patched = patchDashboardJobCaches(queryClient, event.payload);
    if (patched.found) {
      if (isJobTerminal(event.payload.status)) {
        queryClient.invalidateQueries({ queryKey: [...jobFilesQueryRoot, event.payload.id] });
        return applyEventMeta() ? "handled" : "refresh-files-meta";
      }
      return "handled";
    }
    return applyEventMeta() ? "handled" : "refresh-meta";
  }
  return "refresh-workbench";
}

export function useDashboardEvents(queryClient: QueryClient, onRefresh: () => void, enabled = true) {
  const [sseConnected, setSseConnected] = useState(true);

  useEffect(() => {
    if (!enabled) {
      setSseConnected(true);
      return;
    }
    const events = new EventSource("/api/events");
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
    };

    events.onerror = () => {
      setSseConnected(false);
      scheduleRefresh();
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
      metaController?.abort();
    };
  }, [enabled, queryClient, onRefresh]);

  return { sseConnected };
}
