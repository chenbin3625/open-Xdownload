import React from "react";
import type { Job } from "../../lib/api";

const statusCopy: Record<Job["status"], { label: string; tone: string }> = {
  pending: { label: "排队", tone: "muted" },
  resolving: { label: "解析", tone: "processing" },
  downloading: { label: "下载", tone: "processing" },
  completed: { label: "完成", tone: "success" },
  completed_with_errors: { label: "部分失败", tone: "warning" },
  failed: { label: "失败", tone: "danger" },
  canceled: { label: "取消", tone: "muted" },
};

export const JobStatusTag = React.memo(function JobStatusTag({
  status,
}: {
  status: Job["status"];
}) {
  const config = statusCopy[status];
  return <span className={`job-status job-status-${config.tone}`}>{config.label}</span>;
});
