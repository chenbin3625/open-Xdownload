import { useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useCallback, useEffect, useState } from "react";
import {
  archiveScheduleQueryRoot,
  configQueryRoot,
  dashboardMetaQueryRoot,
  getArchiveSchedules,
  getConfig,
  getDashboardMeta,
  getJobsPage,
  jobsQueryRoot,
  type DashboardMeta,
  type JobKind,
  type JobsPage,
} from "./lib/api";
import {
  invalidateWorkbenchQueries,
  mergeNewerJobs,
  useDashboardEvents,
} from "./lib/useDashboardEvents";
import { jobStatusBucket } from "./lib/jobStatus";
import { useBreakpointUp } from "./lib/useMediaQuery";
import { useRouteState } from "./lib/useRouteState";

import { Alert, ListSkeleton } from "./components/ui/Feedback";
import { Drawer } from "./components/ui/Overlay";

import { AppSidebar } from "./components/layout/AppSidebar";
import { AppHeader } from "./components/layout/AppHeader";
import { CreateJobModal } from "./components/modals/CreateJobModal";
import { FailedTweetDrawer } from "./components/drawers/FailedTweetDrawer";
import { GlobalLoadingBar } from "./components/common/GlobalLoadingBar";

import { TaskCenterPage } from "./pages/TaskCenterPage";
import { SchedulesPage } from "./pages/SchedulesPage";
import { GalleryPage } from "./pages/GalleryPage";
import { SettingsPage } from "./pages/SettingsPage";

// 侧边栏的 overview / workbench / tasks 都指向任务调度中心这一个页面。
const taskCenterSections = ["overview", "workbench", "tasks"];

export default function App() {
  const queryClient = useQueryClient();
  // 与 Tailwind 的 lg 断点同源，避免 992–1024px 区间里布局与可见性错位。
  const isCompact = !useBreakpointUp("lg");

  const {
    activeSection,
    jobPage,
    jobPageSize,
    handleSectionChange,
    handleJobPageChange,
    handleJobPageSizeChange,
    syncServerPage,
  } = useRouteState();

  // 模态框与抽屉状态
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [createInitialInput, setCreateInitialInput] = useState("");
  const [createInitialKind, setCreateInitialKind] = useState<JobKind | "schedule">("user");
  const [failedDrawerOpen, setFailedDrawerOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [manualRefreshPending, setManualRefreshPending] = useState(false);

  const refreshDashboard = useCallback(
    () => invalidateWorkbenchQueries(queryClient),
    [queryClient],
  );

  // overview / workbench / tasks 三个入口都渲染任务调度中心，
  // 判定条件此前在 JSX 里又抄了两遍。
  const isTaskCenterActive = taskCenterSections.includes(activeSection);
  const isWorkbenchActive =
    isTaskCenterActive ||
    activeSection === "schedules" ||
    activeSection === "gallery";

  useDashboardEvents(queryClient, refreshDashboard, isWorkbenchActive);

  // 任务分页 Query
  const jobs = useQuery({
    queryKey: [...jobsQueryRoot, jobPage, jobPageSize],
    queryFn: async ({ queryKey, signal }) => {
      const page = await getJobsPage({ page: jobPage, pageSize: jobPageSize, signal });
      // 请求期间可能已有更新的 SSE 补丁落库，逐条比较 updatedAt，别把进度写回旧值。
      return mergeNewerJobs(queryClient.getQueryData<JobsPage>(queryKey), page);
    },
    placeholderData: (previousData) => previousData,
    staleTime: 15_000,
    enabled: isWorkbenchActive,
    refetchInterval: (query) => {
      const page = query.state.data as JobsPage | undefined;
      return page?.items.some((job) => jobStatusBucket(job.status) === "active")
        ? 5_000
        : false;
    },
  });

  // 统计与计数 Query
  const meta = useQuery({
    queryKey: dashboardMetaQueryRoot,
    queryFn: ({ signal }) => getDashboardMeta(signal),
    staleTime: 15_000,
    enabled: isWorkbenchActive,
    refetchInterval: (query) => {
      const data = query.state.data as DashboardMeta | undefined;
      return data && data.stats.active > 0 ? 5_000 : false;
    },
  });

  // 定时计划 Query
  const schedules = useQuery({
    queryKey: archiveScheduleQueryRoot,
    queryFn: ({ signal }) => getArchiveSchedules(signal),
    staleTime: 15_000,
    enabled: isWorkbenchActive,
  });

  // 系统配置 Query
  const config = useQuery({
    queryKey: configQueryRoot,
    queryFn: ({ signal }) => getConfig(signal),
    staleTime: 15_000,
    enabled: activeSection === "settings",
  });

  // 分页边界校验
  useEffect(() => {
    if (jobs.isPlaceholderData || !meta.data) return;
    const totalPages =
      meta.data.stats.total > 0
        ? Math.ceil(meta.data.stats.total / jobPageSize)
        : 1;
    if (jobPage > totalPages) {
      syncServerPage(totalPages);
    }
  }, [jobPage, jobPageSize, jobs.isPlaceholderData, meta.data, syncServerPage]);

  function handleManualRefresh() {
    setManualRefreshPending(true);
    const task =
      activeSection === "settings"
        ? queryClient.invalidateQueries({ queryKey: configQueryRoot })
        : refreshDashboard();
    void task.finally(() => setManualRefreshPending(false));
  }

  function openCreateModal(initial = "", kind: JobKind | "schedule" = "user") {
    setCreateInitialInput(initial);
    setCreateInitialKind(kind);
    setCreateModalOpen(true);
  }

  const jobsData = jobs.data?.items ?? [];
  const currentPagination = {
    page: jobs.data?.page ?? jobPage,
    pageSize: jobs.data?.pageSize ?? jobPageSize,
    total: meta.data?.stats.total ?? 0,
    totalPages: meta.data?.stats.total
      ? Math.ceil(meta.data.stats.total / (jobs.data?.pageSize ?? jobPageSize))
      : 0,
  };

  const currentStats = meta.data?.stats ?? {
    total: 0,
    active: 0,
    completed: 0,
    failed: 0,
  };
  const failedTweetCount = meta.data?.failedTweetCount ?? 0;

  const sidebarProps = {
    activeSection,
    failedTweetCount,
    storageType: config.data?.storageType || "local",
    storagePath: config.data?.downloadDir || "/downloads",
  };

  return (
    <div className="flex h-screen w-screen overflow-hidden bg-canvas">
      {/* 任意接口请求在途时的全局顶部加载进度条 */}
      <GlobalLoadingBar />

      {/* 桌面端常驻侧边栏 */}
      {!isCompact && (
        <AppSidebar
          {...sidebarProps}
          onSectionChange={handleSectionChange}
          onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
        />
      )}

      {/* 移动端抽屉侧边栏 */}
      {isCompact && (
        <Drawer
          side="left"
          width="14.5rem"
          open={mobileMenuOpen}
          onClose={() => setMobileMenuOpen(false)}
        >
          <AppSidebar
            {...sidebarProps}
            onSectionChange={(section) => {
              handleSectionChange(section);
              setMobileMenuOpen(false);
            }}
            onOpenFailedDrawer={() => {
              setMobileMenuOpen(false);
              setFailedDrawerOpen(true);
            }}
          />
        </Drawer>
      )}

      {/* 右侧主视窗内容流 */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <AppHeader
          refreshPending={manualRefreshPending}
          onRefresh={handleManualRefresh}
          onQuickSubmit={(input) => openCreateModal(input)}
          onToggleMobileMenu={() => setMobileMenuOpen(true)}
          showMenuButton={isCompact}
        />

        {/* 页面主内容滚动容器 */}
        <main className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto flex w-full max-w-[100rem] flex-col gap-4 p-4 lg:p-6">
            {/* 异常状态提示 */}
            {isWorkbenchActive && jobs.isError && (
              <Alert
                type="error"
                showIcon
                message="任务数据加载失败"
                description={
                  jobs.error instanceof Error
                    ? jobs.error.message
                    : "请检查后台服务连接"
                }
              />
            )}

            {activeSection === "settings" && config.isError && (
              <Alert
                type="error"
                showIcon
                message="配置数据加载失败"
                description={
                  config.error instanceof Error
                    ? config.error.message
                    : "请检查后台服务连接"
                }
              />
            )}

            {/* 初始加载骨架屏（仅任务中心；归档计划与媒体库由页面内部骨架屏负责） */}
            {isTaskCenterActive && !jobs.data && jobs.isLoading && (
              <ListSkeleton rows={6} avatar={false} />
            )}

            {/* 视图分发：默认首页即为任务调度中心 */}
            {isTaskCenterActive && !(jobs.isLoading && !jobs.data) && (
              <TaskCenterPage
                jobs={jobsData}
                stats={currentStats}
                failedTweetCount={failedTweetCount}
                pagination={currentPagination}
                tableLoading={jobs.isPlaceholderData}
                onPageChange={handleJobPageChange}
                onPageSizeChange={handleJobPageSizeChange}
                onOpenCreateModal={() => openCreateModal()}
                onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
              />
            )}

            {activeSection === "schedules" && (
              <SchedulesPage
                schedules={schedules.data ?? []}
                loading={schedules.isLoading}
                onOpenCreateModal={() => openCreateModal("", "schedule")}
              />
            )}

            {activeSection === "gallery" && <GalleryPage jobs={jobsData} />}

            {/* 设置页加载骨架屏（配置查询在途时避免内容区空白） */}
            {activeSection === "settings" && config.isLoading && (
              <ListSkeleton rows={8} avatar={false} />
            )}

            {activeSection === "settings" && config.data && (
              <SettingsPage
                config={config.data}
                onRefresh={handleManualRefresh}
                refreshPending={manualRefreshPending}
              />
            )}
          </div>
        </main>
      </div>

      {/* 统一新建任务/归档模态框 */}
      <CreateJobModal
        open={createModalOpen}
        onClose={() => setCreateModalOpen(false)}
        initialInput={createInitialInput}
        initialKind={createInitialKind}
      />

      {/* 失败推文重试抽屉 */}
      <FailedTweetDrawer
        open={failedDrawerOpen}
        onClose={() => setFailedDrawerOpen(false)}
        items={[]}
        total={failedTweetCount}
      />
    </div>
  );
}
