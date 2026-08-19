import { Descriptions } from "antd";
import React from "react";
import type { DownloadRecord, FailedMedia, Job } from "../../lib/api";
import { formatDateTime, Stack } from "../common/CommonUI";
import { JobFiles } from "./JobFiles";

export function JobDetails({
  job,
  downloads,
  failed,
}: {
  job: Job;
  downloads: DownloadRecord[];
  failed: FailedMedia[];
}) {
  const fileCount = downloads.length + failed.length;
  return (
    <Stack size={16} style={{ padding: "8px 4px 4px" }}>
      <Descriptions
        size="small"
        column={{ xs: 1, sm: 2, lg: 4 }}
        items={[
          { key: "id", label: "任务 ID", children: `#${job.id}` },
          { key: "created", label: "创建时间", children: formatDateTime(job.createdAt) },
          { key: "updated", label: "更新时间", children: formatDateTime(job.updatedAt) },
          { key: "files", label: "文件数", children: fileCount },
        ]}
      />
      <JobFiles downloads={downloads} failed={failed} />
    </Stack>
  );
}

export function groupDownloadsByJob(downloads: DownloadRecord[]) {
  const grouped = new Map<number, DownloadRecord[]>();
  for (const item of downloads) {
    const items = grouped.get(item.jobId) ?? [];
    items.push(item);
    grouped.set(item.jobId, items);
  }
  return grouped;
}

export function groupFailedMediaByJob(failed: FailedMedia[]) {
  const grouped = new Map<number, FailedMedia[]>();
  for (const item of failed) {
    const items = grouped.get(item.jobId) ?? [];
    items.push(item);
    grouped.set(item.jobId, items);
  }
  return grouped;
}
