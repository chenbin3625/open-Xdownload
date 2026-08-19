import { useEffect, useState } from "react";
import type { QueryClient } from "@tanstack/react-query";
import type { Dashboard, Job } from "./api";
import {
  dashboardStatsWithJobStatusChange,
  isJobTerminal,
  jobStatusBucket,
} from "./jobStatus";

export const dashboardQueryRoot = ["dashboard"] as const;

export type DashboardEvent = {
  type?: string;
  jobId?: number;
  payload?: unknown;
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

  queryClient.setQueriesData<Dashboard>({ queryKey: dashboardQueryRoot }, (current) => {
    if (!current) return current;

    const jobIndex = current.jobs.findIndex((job) => job.id === updatedJob.id);
    if (jobIndex === -1) return current;

    found = true;
    const previousJob = current.jobs[jobIndex];
    if (
      jobStatusBucket(previousJob.status) !== jobStatusBucket(updatedJob.status) ||
      isJobTerminal(updatedJob.status)
    ) {
      needsFullRefresh = true;
    }
    if (sameJob(previousJob, updatedJob)) return current;

    const jobs = [...current.jobs];
    jobs[jobIndex] = updatedJob;
    return {
      ...current,
      jobs,
      stats: dashboardStatsWithJobStatusChange(current.stats, previousJob.status, updatedJob.status),
    };
  });

  return { found, needsFullRefresh };
}

export function useDashboardEvents(queryClient: QueryClient, onRefresh: () => void) {
  const [sseConnected, setSseConnected] = useState(true);

  useEffect(() => {
    const events = new EventSource("/api/events");
    let timer: ReturnType<typeof setTimeout> | null = null;

    const scheduleRefresh = () => {
      if (timer) return;
      timer = setTimeout(() => {
        timer = null;
        onRefresh();
      }, 500);
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
      if (event.type === "job.updated" && isDashboardJobPayload(event.payload)) {
        const result = patchDashboardJobCaches(queryClient, event.payload);
        if (!result.found || result.needsFullRefresh) {
          scheduleRefresh();
        }
        return;
      }
      scheduleRefresh();
    };

    return () => {
      events.close();
      if (timer) clearTimeout(timer);
    };
  }, [queryClient, onRefresh]);

  return { sseConnected };
}
