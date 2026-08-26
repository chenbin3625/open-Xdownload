import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useState } from "react";
import {
  archiveScheduleQueryRoot,
  deleteArchiveSchedule,
  getArchiveSchedules,
  runArchiveSchedule,
  updateArchiveSchedule,
  type ArchiveSchedule,
} from "../../lib/api";
import { formatDateTime, formatIntervalMinutes, getErrorMessage, kindLabel } from "../../lib/format";
import { toast } from "../../lib/toast";
import { prependJobsToCaches } from "../../lib/useDashboardEvents";

export const ArchiveScheduleList = React.memo(function ArchiveScheduleList() {
  const queryClient = useQueryClient();
  const [page, setPage] = useState(1);
  const schedulesQuery = useQuery({
    queryKey: archiveScheduleQueryRoot,
    queryFn: ({ signal }) => getArchiveSchedules(signal),
    staleTime: 15_000,
  });
  const schedules = schedulesQuery.data ?? [];
  const refreshSchedules = () => {
    queryClient.invalidateQueries({ queryKey: archiveScheduleQueryRoot });
  };

  const runSchedule = useMutation({
    mutationFn: runArchiveSchedule,
    onSuccess: (jobs) => {
      refreshSchedules();
      prependJobsToCaches(queryClient, jobs);
      toast("计划已开始运行", { description: `已创建 ${jobs.length} 个任务` });
    },
    onError: (error) => {
      toast("运行失败", { description: getErrorMessage(error), tone: "err" });
    },
  });
  const toggleSchedule = useMutation({
    mutationFn: ({ schedule, enabled }: { schedule: ArchiveSchedule; enabled: boolean }) =>
      updateArchiveSchedule(schedule.id, {
        name: schedule.name,
        enabled,
        intervalMinutes: schedule.intervalMinutes,
        items: schedule.items,
      }),
    onSuccess: () => {
      refreshSchedules();
      toast("计划已更新");
    },
    onError: (error) => {
      toast("更新失败", { description: getErrorMessage(error), tone: "err" });
    },
  });
  const removeSchedule = useMutation({
    mutationFn: deleteArchiveSchedule,
    onSuccess: () => {
      refreshSchedules();
      toast("计划已删除");
    },
    onError: (error) => {
      toast("删除失败", { description: getErrorMessage(error), tone: "err" });
    },
});
  if (schedulesQuery.isLoading && schedules.length === 0) {
    return <div className="shell-skeleton-block" />;
  }
  if (schedules.length === 0) {
    return <p className="job-empty">暂无定时计划</p>;
  }

  const pageSize = 5;
  const totalPages = Math.max(1, Math.ceil(schedules.length / pageSize));
  const current = Math.min(page, totalPages);
  const items = schedules.slice((current - 1) * pageSize, current * pageSize);

  return (
    <div className="schedule-list">
      <ul>
        {items.map((schedule) => {
          const targets = schedule.items.map((item) => `${kindLabel(item.kind)} ${item.input}`).join("，");
          return (
            <li key={schedule.id} className="schedule-item">
              <div className="schedule-copy">
                <div className="job-title-cell">
                  <strong>{schedule.name}</strong>
                  <span className={schedule.enabled ? "job-status job-status-processing" : "job-status"}>
                    {schedule.enabled ? "启用" : "停用"}
                  </span>
                  <span className="job-kind-tag">{formatIntervalMinutes(schedule.intervalMinutes)}</span>
                </div>
                <div className="schedule-meta">
                  <span>目标 {schedule.items.length}</span>
                  <span>下次 {formatDateTime(schedule.nextRunAt)}</span>
                  <span>上次 {schedule.lastRunAt ? formatDateTime(schedule.lastRunAt) : "未运行"}</span>
                </div>
                <div className="job-ellipsis" title={targets}>{targets || "无目标"}</div>
              </div>
              <div className="schedule-actions">
                <label className="schedule-switch">
                  <input
                    type="checkbox"
                    checked={schedule.enabled}
                    disabled={toggleSchedule.isPending && toggleSchedule.variables?.schedule.id === schedule.id}
                    onChange={(event) => toggleSchedule.mutate({ schedule, enabled: event.target.checked })}
                  />
                  <span className="visually-hidden">启用计划</span>
                </label>
                <button
                  type="button"
                  className="job-text-btn"
                  disabled={runSchedule.isPending && runSchedule.variables === schedule.id}
                  onClick={() => runSchedule.mutate(schedule.id)}
                >
                  运行
                </button>
                <button
                  type="button"
                  className="job-text-btn is-danger"
                  disabled={removeSchedule.isPending && removeSchedule.variables === schedule.id}
                  onClick={() => {
                    if (window.confirm("确认删除这个定时计划？")) {
                      removeSchedule.mutate(schedule.id);
                    }
                  }}
                >
                  删除
                </button>
              </div>
            </li>
          );
        })}
      </ul>
      {totalPages > 1 ? (
        <div className="shell-pagination">
          <button type="button" className="shell-page-btn" disabled={current <= 1} onClick={() => setPage(current - 1)}>
            上一页
          </button>
          <span>{current}/{totalPages}</span>
          <button type="button" className="shell-page-btn" disabled={current >= totalPages} onClick={() => setPage(current + 1)}>
            下一页
          </button>
        </div>
      ) : null}
    </div>
  );
});
