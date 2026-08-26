import { useQuery } from "@tanstack/react-query";
import { Descriptions } from "antd";
import React from "react";
import { getJobFiles, jobFilesQueryRoot, type DownloadRecord, type FailedMedia, type Job } from "../../lib/api";
import { isJobTerminal } from "../../lib/jobStatus";
import { formatDateTime, ListSkeleton, Stack } from "../common/CommonUI";
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
  const filesQuery = useQuery({
    queryKey: [...jobFilesQueryRoot, job.id],
    queryFn: ({ signal }) => getJobFiles(job.id, signal),
    enabled: isJobTerminal(job.status),
    staleTime: 60_000,
  });
  const resolvedDownloads = filesQuery.data?.downloads ?? downloads;
  const resolvedFailed = filesQuery.data?.failed ?? failed;
  const fileCount = resolvedDownloads.length + resolvedFailed.length;
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
      {isJobTerminal(job.status) && filesQuery.isLoading ? <ListSkeleton rows={2} /> : (
        <JobFiles downloads={resolvedDownloads} failed={resolvedFailed} />
      )}
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
