import { describe, expect, it } from "vitest";
import {
  cancelableStatuses,
  dashboardStatsWithJobStatusChange,
  isJobTerminal,
  jobStatusBucket,
  progressStatus,
  retryableStatuses,
} from "./jobStatus";
import type { Dashboard, Job } from "./api";

const baseStats: Dashboard["stats"] = { total: 10, active: 3, completed: 4, failed: 2 };

function job(status: Job["status"]): Job {
  return {
    id: 1,
    kind: "tweet_link",
    status,
    input: "https://x.com/u/status/1",
    title: "t",
    progress: 0.5,
    message: "",
    createdAt: "2024-01-01T00:00:00Z",
    updatedAt: "2024-01-01T00:00:00Z",
  };
}

describe("jobStatus buckets", () => {
  it("classifies each status into the right bucket", () => {
    expect(cancelableStatuses).toEqual(["pending", "resolving", "downloading"]);
    expect(retryableStatuses).toEqual(["failed", "canceled", "completed", "completed_with_errors"]);
    expect(jobStatusBucket("pending")).toBe("active");
    expect(jobStatusBucket("downloading")).toBe("active");
    expect(jobStatusBucket("completed")).toBe("completed");
    expect(jobStatusBucket("failed")).toBe("failed");
    expect(jobStatusBucket("completed_with_errors")).toBe("failed");
    expect(jobStatusBucket("canceled")).toBe("idle");
  });

  it("marks terminal statuses", () => {
    for (const status of ["completed", "completed_with_errors", "failed", "canceled"] as const) {
      expect(isJobTerminal(status)).toBe(true);
    }
    for (const status of ["pending", "resolving", "downloading"] as const) {
      expect(isJobTerminal(status)).toBe(false);
    }
  });
});
describe("dashboardStatsWithJobStatusChange", () => {
  it("moves a job between buckets", () => {
    const next = dashboardStatsWithJobStatusChange(baseStats, "downloading", "completed");
    expect(next).toEqual({ total: 10, active: 2, completed: 5, failed: 2 });
  });

  it("moves a job into failed", () => {
    const next = dashboardStatsWithJobStatusChange(baseStats, "resolving", "failed");
    expect(next).toEqual({ total: 10, active: 2, completed: 4, failed: 3 });
  });

  it("returns the same stats object when bucket is unchanged", () => {
    const next = dashboardStatsWithJobStatusChange(baseStats, "pending", "downloading");
    expect(next).toBe(baseStats);
  });

  it("never goes below zero", () => {
    const empty: Dashboard["stats"] = { total: 1, active: 0, completed: 0, failed: 0 };
    const next = dashboardStatsWithJobStatusChange(empty, "pending", "completed");
    expect(next.active).toBe(0);
  });
});

describe("progressStatus", () => {
  it("maps statuses to progress states", () => {
    expect(progressStatus(job("failed"))).toBe("exception");
    expect(progressStatus(job("completed_with_errors"))).toBe("exception");
    expect(progressStatus(job("completed"))).toBe("success");
    expect(progressStatus(job("downloading"))).toBe("active");
    expect(progressStatus(job("resolving"))).toBe("active");
    expect(progressStatus(job("pending"))).toBe("normal");
    expect(progressStatus(job("canceled"))).toBe("normal");
  });
});
