import { t, useTranslation } from "./lib/i18n";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useCallback, useEffect, useState } from "react";
import { Layout } from "@douyinfe/semi-ui";
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
import { useBreakpointUp } from "./lib/useMediaQuery";
import { useRouteState, type SectionKey } from "./lib/useRouteState";

import { Alert, ListSkeleton } from "./components/semi";
import { Drawer } from "./components/semi";

import { AppMobileNavigation, AppSidebar } from "./components/layout/AppSidebar";
import { AppHeader } from "./components/layout/AppHeader";
import { CreateJobModal } from "./components/modals/CreateJobModal";
import { FailedTweetDrawer } from "./components/drawers/FailedTweetDrawer";
import { GlobalLoadingBar } from "./components/common/GlobalLoadingBar";

import { TaskCenterPage } from "./pages/TaskCenterPage";

// 任务中心是首屏，保持静态；其余页面按路由懒加载，减小首屏 bundle。
const SchedulesPage = React.lazy(() => import("./pages/SchedulesPage").then(module => ({ default: module.SchedulesPage })));
const GalleryPage = React.lazy(() => import("./pages/GalleryPage").then(module => ({ default: module.GalleryPage })));
const SettingsPage = React.lazy(() => import("./pages/SettingsPage").then(module => ({ default: module.SettingsPage })));
const TweetsPage = React.lazy(() => import("./pages/TweetsPage").then(module => ({ default: module.TweetsPage })));
import { AuthGate } from "./components/auth/AuthGate";

// 侧边栏的 overview / workbench / tasks 都指向任务调度中心这一个页面。
const taskCenterSections = ["overview", "workbench", "tasks"];

export default function App() {
  useTranslation();
  return <AuthGate><Workbench /></AuthGate>;
}

function Workbench() {
  useTranslation();
  const queryClient = useQueryClient();
  // 与 Tailwind 的 lg 断点同源，避免 992–1024px 区间里布局与可见性错位。
  const isCompact = !useBreakpointUp("lg");

  const {
    activeSection,
    jobPage,
    jobPageSize,
    jobStatus,
    jobKind,
    jobSearch,
    gallery,
    handleSectionChange: navigateSection,
    handleJobPageChange,
    handleJobPageSizeChange,
    handleJobStatusChange,
    handleJobKindChange,
    handleJobSearchChange,
    handleGalleryChange,
    syncServerPage,
  } = useRouteState();

  const handleSectionChange = (section: SectionKey) => {
    if (section !== activeSection && !window.dispatchEvent(new Event("settings:before-navigate", { cancelable: true })) && !window.confirm(t("放弃未保存的修改？"))) return;
    navigateSection(section);
  };

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
    activeSection === "gallery" || activeSection === "tweets";

  const { sseConnected, reconnect: reconnectEvents } = useDashboardEvents(
    queryClient,
    refreshDashboard,
    isWorkbenchActive,
  );

  // 任务分页 Query
  const jobs = useQuery({
    queryKey: [...jobsQueryRoot, jobPage, jobPageSize, jobStatus, jobKind, jobSearch],
    queryFn: async ({ queryKey, signal }) => {
      const page = await getJobsPage({ page: jobPage, pageSize: jobPageSize, status: jobStatus, kind: jobKind, search: jobSearch, signal });
      // 请求期间可能已有更新的 SSE 补丁落库，逐条比较 updatedAt，别把进度写回旧值。
      // 已筛选页必须以服务端成员集合为准，避免状态变更后把不匹配的旧任务插回来。
      return jobStatus === "all" && jobKind === "all" && !jobSearch
        ? mergeNewerJobs(queryClient.getQueryData<JobsPage>(queryKey), page)
        : page;
    },
    placeholderData: (previousData) => previousData,
    staleTime: 15_000,
    enabled: isWorkbenchActive,
    // SSE 已连接时进度由事件推送，轮询只做兜底；断线才回到 5s。
    refetchInterval: () => (sseConnected ? 30_000 : 5_000),
  });

  // 统计与计数 Query
  const meta = useQuery({
    queryKey: dashboardMetaQueryRoot,
    queryFn: ({ signal }) => getDashboardMeta(signal),
    placeholderData: (previousData) => previousData,
    staleTime: 15_000,
    enabled: isWorkbenchActive,
    refetchInterval: () => (sseConnected ? 30_000 : 5_000),
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
    if (jobs.isPlaceholderData || !jobs.data) return;
    const total = jobs.data.total ?? (jobStatus === "all" && jobKind === "all" && !jobSearch ? meta.data?.stats.total ?? 0 : 0);
    const totalPages = total
      ? Math.ceil(total / jobPageSize)
      : 1;
    if (jobPage > totalPages) {
      syncServerPage(totalPages);
    }
  }, [jobPage, jobPageSize, jobStatus, jobKind, jobSearch, jobs.data, jobs.isPlaceholderData, meta.data, syncServerPage]);

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
  const filteredTotal = jobs.data?.total ?? (jobStatus === "all" && jobKind === "all" && !jobSearch ? meta.data?.stats.total ?? 0 : 0);
  const currentPagination = {
    page: jobs.data?.page ?? jobPage,
    pageSize: jobs.data?.pageSize ?? jobPageSize,
    total: filteredTotal,
    totalPages: filteredTotal
      ? Math.ceil(filteredTotal / (jobs.data?.pageSize ?? jobPageSize))
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
  };

  return (
    <Layout hasSider={!isCompact} className="app-shell flex h-dvh w-full overflow-hidden bg-canvas">
      {/* 任意接口请求在途时的全局顶部加载进度条 */}
      <GlobalLoadingBar />

      {/* 桌面端常驻侧边栏 */}
      {!isCompact && (
        <AppSidebar
          {...sidebarProps}
          onSectionChange={handleSectionChange}
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
          />
        </Drawer>
      )}

      {/* 右侧主视窗内容流 */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <AppHeader
          failedTweetCount={failedTweetCount}
          onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
          refreshPending={manualRefreshPending}
          onRefresh={handleManualRefresh}
          onQuickSubmit={(input) => openCreateModal(input)}
          onToggleMobileMenu={() => setMobileMenuOpen(true)}
          showMenuButton={isCompact}
          realtimeDisconnected={!sseConnected}
          onReconnectRealtime={reconnectEvents}
        />

        {/* 页面主内容滚动容器 */}
        <main className="app-main min-h-0 flex-1 overflow-y-auto">
          <div className="page-content mx-auto flex w-full min-w-0 max-w-[100rem] flex-col gap-4 p-3 sm:p-4 lg:p-6">
            {/* 异常状态提示 */}
            {isWorkbenchActive && jobs.isError && (
              <Alert
                type="error"
                showIcon
                message={t("任务数据加载失败")}
                description={
                  jobs.error instanceof Error
                    ? jobs.error.message
                    : t("请检查后台服务连接")
                }
              />
            )}

            {isWorkbenchActive && meta.isError && (
              <Alert type="error" showIcon message={t("任务统计加载失败")} description={meta.error.message} />
            )}

            {activeSection === "settings" && config.isError && (
              <Alert
                type="error"
                showIcon
                message={t("配置数据加载失败")}
                description={
                  config.error instanceof Error
                    ? config.error.message
                    : t("请检查后台服务连接")
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
                statusFilter={jobStatus}
                onStatusFilterChange={handleJobStatusChange}
                kindFilter={jobKind}
                onKindFilterChange={handleJobKindChange}
                searchKeyword={jobSearch}
                onSearchKeywordChange={handleJobSearchChange}
                filterCounts={jobs.data?.counts}
                onOpenCreateModal={() => openCreateModal()}
                onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
              />
            )}

            {activeSection === "schedules" && (
              <React.Suspense fallback={<ListSkeleton rows={6} avatar={false} />}>
              <SchedulesPage
                schedules={schedules.data ?? []}
                loading={schedules.isLoading}
                error={schedules.isError ? schedules.error.message : undefined}
                onOpenCreateModal={() => openCreateModal("", "schedule")}
              />
              </React.Suspense>
            )}

            {activeSection === "gallery" && (
              <React.Suspense fallback={<ListSkeleton rows={6} avatar={false} />}>
                <GalleryPage filter={gallery} onFilterChange={handleGalleryChange} />
              </React.Suspense>
            )}
            {activeSection === "tweets" && (
              <React.Suspense fallback={<ListSkeleton rows={6} avatar={false} />}>
                <TweetsPage realtimeConnected={sseConnected} />
              </React.Suspense>
            )}

            {/* 设置页加载骨架屏（配置查询在途时避免内容区空白） */}
            {activeSection === "settings" && config.isLoading && (
              <ListSkeleton rows={8} avatar={false} />
            )}

            {activeSection === "settings" && config.data && (
              <React.Suspense fallback={<ListSkeleton rows={8} avatar={false} />}>
                <SettingsPage
                  config={config.data}
                  onRefresh={handleManualRefresh}
                  refreshPending={manualRefreshPending}
                />
              </React.Suspense>
            )}
          </div>
        </main>
        {isCompact && <AppMobileNavigation activeSection={activeSection} onSectionChange={handleSectionChange} />}
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
        total={failedTweetCount}
      />
    </Layout>
  );
}
