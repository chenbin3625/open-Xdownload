import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  parseDashboardEvent,
  isDashboardJobPayload,
  isDashboardJobList,
  patchDashboardJobCaches,
  applyDashboardMeta,
  applyDashboardEvent,
  prependJobsToCaches,
  sameJob,
} from "./useDashboardEvents";
import {
  dashboardMetaQueryRoot,
  jobsQueryRoot,
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
