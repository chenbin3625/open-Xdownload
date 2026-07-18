import type { Dashboard, Job } from "./api";

export const cancelableStatuses: Job["status"][] = ["pending", "resolving", "downloading"];
export const retryableStatuses: Job["status"][] = ["failed", "canceled", "completed", "completed_with_errors"];

export function jobStatusBucket(status: Job["status"]) {
  if (cancelableStatuses.includes(status)) return "active";
  if (status === "completed") return "completed";
  if (status === "failed" || status === "completed_with_errors") return "failed";
  return "idle";
}

export function isJobTerminal(status: Job["status"]) {
  return status === "completed" || status === "completed_with_errors" || status === "failed" || status === "canceled";
}

export function dashboardStatsWithJobStatusChange(
  stats: Dashboard["stats"],
  previousStatus: Job["status"],
  nextStatus: Job["status"],
) {
  const previousBucket = jobStatusBucket(previousStatus);
  const nextBucket = jobStatusBucket(nextStatus);
  if (previousBucket === nextBucket) return stats;

  const nextStats = { ...stats };
  if (previousBucket === "active") nextStats.active = Math.max(0, nextStats.active - 1);
  if (previousBucket === "completed") nextStats.completed = Math.max(0, nextStats.completed - 1);
  if (previousBucket === "failed") nextStats.failed = Math.max(0, nextStats.failed - 1);
  if (nextBucket === "active") nextStats.active += 1;
  if (nextBucket === "completed") nextStats.completed += 1;
  if (nextBucket === "failed") nextStats.failed += 1;
  return nextStats;
}

export function progressStatus(job: Job): "success" | "exception" | "normal" | "active" {
  if (job.status === "failed" || job.status === "completed_with_errors") return "exception";
  if (job.status === "completed") return "success";
  if (job.status === "downloading" || job.status === "resolving") return "active";
  return "normal";
}
