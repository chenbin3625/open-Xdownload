import { useQuery } from "@tanstack/react-query";
import React, { useCallback, useEffect, useState } from "react";
import { JobTable } from "../components/jobs/JobTable";
import { FailedTweetQueue } from "../components/jobs/FailedTweetQueue";
import { StatsSummary } from "../components/workbench/StatsSummary";
import { TweetParser } from "../components/workbench/TweetParser";
import { ArchiveScheduleList } from "../components/workbench/ArchiveScheduleList";
import { BatchDownloadLauncher } from "../components/workbench/BatchDownloadLauncher";
import { ShellDrawer } from "../components/common/ShellUI";
import {
  archiveScheduleQueryRoot,
  getArchiveSchedules,
  type DashboardMeta,
  type DashboardPagination,
  type JobsPage,
} from "../lib/api";

export default function OverviewPage({
  jobs,
  meta,
  onJobPageChange,
  onJobPageSizeChange,
}: {
  jobs: JobsPage;
  meta?: DashboardMeta;
  onJobPageChange: (page: number) => void;
  onJobPageSizeChange: (pageSize: number) => void;
}) {
  const [batchDrawerOpen, setBatchDrawerOpen] = useState(false);
  const [failedDrawerOpen, setFailedDrawerOpen] = useState(false);
  const failedTweetCount = meta?.failedTweetCount ?? 0;
  const schedules = useQuery({
    queryKey: archiveScheduleQueryRoot,
    queryFn: ({ signal }) => getArchiveSchedules(signal),
    staleTime: 15_000,
  });
  const pagination: DashboardPagination = {
    page: jobs.page,
    pageSize: jobs.pageSize,
    total: meta?.stats.total ?? 0,
    totalPages: meta && meta.stats.total > 0 ? Math.ceil(meta.stats.total / jobs.pageSize) : 0,
  };

  const handleOpenBatchDrawer = useCallback(() => setBatchDrawerOpen(true), []);
  const handleCloseBatchDrawer = useCallback(() => setBatchDrawerOpen(false), []);
  const handleOpenFailedDrawer = useCallback(() => setFailedDrawerOpen(true), []);
  const handleCloseFailedDrawer = useCallback(() => setFailedDrawerOpen(false), []);

  useEffect(() => {
    if (failedTweetCount === 0) {
      setFailedDrawerOpen(false);
    }
  }, [failedTweetCount]);

  return (
    <div className="workbench-page">
      {meta?.stats ? (
        <StatsSummary
          stats={meta.stats}
          failedTweetCount={failedTweetCount}
          onOpenFailedDrawer={handleOpenFailedDrawer}
        />
      ) : null}

      <div className="workbench-grid">
        <div className="workbench-main">
          <section className="workbench-panel">
            <div className="workbench-panel-header">
              <div className="workbench-panel-heading">
                <span className="workbench-panel-icon" aria-hidden="true">↓</span>
                <span className="workbench-panel-title">
                  <strong>单条解析</strong>
                  <span>推文媒体</span>
                </span>
              </div>
              <button type="button" className="shell-primary-btn" onClick={handleOpenBatchDrawer}>
                批量归档
              </button>
            </div>
            <TweetParser />
          </section>

          <section className="section-block">
            <div className="section-heading">
              <strong>任务列表</strong>
              {failedTweetCount > 0 ? (
                <button type="button" className="shell-danger-btn" onClick={handleOpenFailedDrawer}>
                  查看失败项
                  <span className="shell-count">{failedTweetCount > 999 ? "999+" : failedTweetCount}</span>
                </button>
              ) : null}
            </div>
            <JobTable
              jobs={jobs.items}
              pagination={pagination}
              onPageChange={onJobPageChange}
              onPageSizeChange={onJobPageSizeChange}
            />
          </section>
        </div>

        <aside className="workbench-rail">
          <section className="workbench-panel workbench-panel-compact">
            <div className="workbench-panel-header">
              <div className="workbench-panel-heading">
                <span className="workbench-panel-icon" aria-hidden="true">↻</span>
                <span className="workbench-panel-title">
                  <strong>定时计划</strong>
                  <span>自动归档</span>
                </span>
              </div>
              <span className="shell-count shell-count-static">{schedules.data?.length ?? 0}</span>
            </div>
            <ArchiveScheduleList />
          </section>
        </aside>
      </div>

      <ShellDrawer open={batchDrawerOpen} title="批量归档" size={920} onClose={handleCloseBatchDrawer}>
        <BatchDownloadLauncher />
      </ShellDrawer>

      <ShellDrawer open={failedDrawerOpen} title="失败推文队列" size={760} onClose={handleCloseFailedDrawer}>
        <FailedTweetQueue total={failedTweetCount} />
      </ShellDrawer>
    </div>
  );
}
