import { useQuery } from "@tanstack/react-query";
import React from "react";
import { getJobFiles, jobFilesQueryRoot, type Job } from "../../lib/api";
import { formatDateTime } from "../../lib/format";
import { isJobTerminal } from "../../lib/jobStatus";
import { JobFiles } from "./JobFiles";

export const JobDetails = React.memo(function JobDetails({ job }: { job: Job }) {
  const terminal = isJobTerminal(job.status);
  const files = useQuery({
    queryKey: [...jobFilesQueryRoot, job.id],
    queryFn: ({ signal }) => getJobFiles(job.id, signal),
    staleTime: 10_000,
    enabled: terminal,
  });
  const downloads = files.data?.downloads ?? [];
  const failed = files.data?.failed ?? [];
  const fileCount = terminal ? downloads.length + failed.length : undefined;

  return (
    <div className="job-details">
      <dl className="job-meta">
        <div>
          <dt>任务 ID</dt>
          <dd>#{job.id}</dd>
        </div>
        <div>
          <dt>创建时间</dt>
          <dd>{formatDateTime(job.createdAt)}</dd>
        </div>
        <div>
          <dt>更新时间</dt>
          <dd>{formatDateTime(job.updatedAt)}</dd>
        </div>
        <div>
          <dt>文件数</dt>
          <dd>{!terminal ? "进行中" : files.isLoading ? "…" : fileCount}</dd>
        </div>
      </dl>
      {!terminal ? <p className="job-empty">任务完成后加载文件记录</p> : files.isLoading ? <div className="shell-skeleton-block" /> : <JobFiles downloads={downloads} failed={failed} />}
    </div>
  );
});
