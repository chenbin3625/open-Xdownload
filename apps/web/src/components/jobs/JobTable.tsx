import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  cancelJob,
  jobFilesQueryRoot,
  jobsQueryRoot,
  retryJob,
  type DashboardPagination,
  type Job,
  type JobsPage,
} from "../../lib/api";
import { clampPercent, formatDateTime, getErrorMessage, kindLabel } from "../../lib/format";
import { tablePageSizeOptions } from "../../lib/pagination";
import { toast } from "../../lib/toast";
import {
  cancelableStatuses,
  progressStatus,
  retryableStatuses,
} from "../../lib/jobStatus";
import { ShellPagination } from "../common/ShellUI";
import {
  patchDashboardJobCaches,
  prependJobsToCaches,
} from "../../lib/useDashboardEvents";
import { JobDetails } from "./JobDetails";
import { JobStatusTag } from "./JobStatusTag";

export const JobTable = React.memo(function JobTable({
  jobs,
  pagination,
  onPageChange,
  onPageSizeChange,
}: {
  jobs: Job[];
  pagination: DashboardPagination;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
}) {
  const queryClient = useQueryClient();
  const [expandedJobIds, setExpandedJobIds] = useState<number[]>([]);
  const manuallyCollapsedIds = useRef<Set<number>>(new Set());
  const activeJobIds = useMemo(
    () => jobs.filter((job) => cancelableStatuses.includes(job.status)).map((job) => job.id),
    [jobs],
  );

  useEffect(() => {
    if (activeJobIds.length === 0) return;
    setExpandedJobIds((current) => {
      const next = new Set(current);
      for (const id of activeJobIds) {
        if (!manuallyCollapsedIds.current.has(id)) {
          next.add(id);
        }
      }
      return next.size === current.length && current.every((id) => next.has(id)) ? current : [...next];
    });
  }, [activeJobIds]);

  useEffect(() => {
    if (manuallyCollapsedIds.current.size === 0) return;
    const active = new Set(activeJobIds);
    for (const id of manuallyCollapsedIds.current) {
      if (!active.has(id)) {
        manuallyCollapsedIds.current.delete(id);
      }
    }
  }, [activeJobIds]);

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: (job) => {
      prependJobsToCaches(queryClient, [job]);
      toast(`已创建重试任务 #${job.id}`);
    },
    onError: (error) => {
      toast("重试失败", { description: getErrorMessage(error), tone: "err" });
    },
  });
  const cancel = useMutation({
    mutationFn: cancelJob,
    onMutate: async (id) => {
      await queryClient.cancelQueries({ queryKey: jobsQueryRoot });
      const snapshots = queryClient.getQueriesData<JobsPage>({ queryKey: jobsQueryRoot });
      queryClient.setQueriesData<JobsPage>({ queryKey: jobsQueryRoot }, (current) => {
        if (!current) return current;
        const jobIndex = current.items.findIndex((job) => job.id === id);
        if (jobIndex === -1) return current;
        const items = [...current.items];
        items[jobIndex] = { ...items[jobIndex], status: "canceled", message: "正在取消" };
        return { ...current, items };
      });
      return { snapshots };
    },
    onSuccess: (job) => {
      patchDashboardJobCaches(queryClient, job);
      void queryClient.invalidateQueries({ queryKey: [...jobFilesQueryRoot, job.id] });
      toast("任务已取消");
    },
    onError: (error, _id, context) => {
      for (const [key, data] of context?.snapshots ?? []) {
        queryClient.setQueryData(key, data);
      }
      toast("取消失败", { description: getErrorMessage(error), tone: "err" });
    },
  });

  const toggleExpanded = useCallback((id: number) => {
    setExpandedJobIds((current) => {
      if (current.includes(id)) {
        manuallyCollapsedIds.current.add(id);
        return current.filter((jobId) => jobId !== id);
      }
      manuallyCollapsedIds.current.delete(id);
      return [...current, id];
    });
  }, []);

  return (
    <div className="job-table-stack">
      <div className="job-table-scroller">
        <table className="job-table">
          <thead>
            <tr>
              <th className="job-col-toggle" />
              <th>任务</th>
              <th className="job-col-status">状态</th>
              <th>进度</th>
              <th className="job-col-updated">更新</th>
              <th className="job-col-actions">操作</th>
            </tr>
          </thead>
          <tbody>
            {jobs.length === 0 ? (
              <tr>
                <td colSpan={6} className="job-empty">暂无任务</td>
              </tr>
            ) : (
              jobs.map((job) => (
                <JobRow
                  key={job.id}
                  job={job}
                  expanded={expandedJobIds.includes(job.id)}
                  canceling={cancel.isPending && cancel.variables === job.id}
                  retrying={retry.isPending && retry.variables === job.id}
                  onToggle={toggleExpanded}
                  onCancel={cancel.mutate}
                  onRetry={retry.mutate}
                />
              ))
            )}
          </tbody>
        </table>
      </div>
      <ShellPagination
        current={pagination.total > 0 ? pagination.page : 1}
        itemName="个任务"
        pageSize={pagination.pageSize}
        pageSizeOptions={tablePageSizeOptions}
        total={pagination.total}
        onChange={(page, pageSize) => {
          if (pageSize !== pagination.pageSize) {
            onPageSizeChange(pageSize);
            return;
          }
          onPageChange(page);
        }}
      />
    </div>
  );
});

const JobRow = React.memo(function JobRow({
  job,
  expanded,
  canceling,
  retrying,
  onToggle,
  onCancel,
  onRetry,
}: {
  job: Job;
  expanded: boolean;
  canceling: boolean;
  retrying: boolean;
  onToggle: (id: number) => void;
  onCancel: (id: number) => void;
  onRetry: (id: number) => void;
}) {
  const canCancel = cancelableStatuses.includes(job.status);
  const canRetry = retryableStatuses.includes(job.status);
  const percent = clampPercent(job.progress);
  const tone = progressStatus(job);
  return (
    <>
      <tr className={expanded ? "job-row is-expanded" : "job-row"}>
        <td>
          <button
            type="button"
            className="job-expand-btn"
            aria-expanded={expanded}
            aria-label={expanded ? "收起详情" : "展开详情"}
            onClick={() => onToggle(job.id)}
          >
            {expanded ? "▾" : "▸"}
          </button>
        </td>
        <td>
          <div className="job-title-cell">
            <strong>{job.title || kindLabel(job.kind)}</strong>
            <span className="job-kind-tag">{kindLabel(job.kind)}</span>
          </div>
          <div className="job-ellipsis" title={job.input}>{job.input}</div>
        </td>
        <td><JobStatusTag status={job.status} /></td>
        <td>
          <div className={`job-progress job-progress-${tone}`} role="progressbar" aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100}>
            <span style={{ width: `${percent}%` }} />
          </div>
          <div className={job.error ? "job-ellipsis is-danger" : "job-ellipsis"} title={job.error || job.message}>
            {job.error || job.message || "暂无消息"}
          </div>
        </td>
        <td className="job-updated">{formatDateTime(job.updatedAt)}</td>
        <td>
          <div className="job-actions">
            <button
              type="button"
              className="job-icon-btn"
              title={canCancel ? "取消" : "当前状态不能取消"}
              disabled={!canCancel || canceling}
              onClick={() => onCancel(job.id)}
            >
              {canceling ? "…" : "停"}
            </button>
            <button
              type="button"
              className="job-icon-btn"
              title={canRetry ? "新建重试任务" : "运行中任务不能重试"}
              disabled={!canRetry || retrying}
              onClick={() => onRetry(job.id)}
            >
              {retrying ? "…" : "重"}
            </button>
          </div>
        </td>
      </tr>
      {expanded ? (
        <tr className="job-expand-row">
          <td colSpan={6}>
            <JobDetails job={job} />
          </td>
        </tr>
      ) : null}
    </>
  );
}, (prev, next) => (
  prev.job === next.job
  && prev.expanded === next.expanded
  && prev.canceling === next.canceling
  && prev.retrying === next.retrying
  && prev.onToggle === next.onToggle
  && prev.onCancel === next.onCancel
  && prev.onRetry === next.onRetry
));
