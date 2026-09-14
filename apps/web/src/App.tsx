import { Alert, ConfigProvider, Drawer, Grid, Skeleton, theme as antdTheme } from "antd";
import zhCN from "antd/locale/zh_CN";
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
import { useRouteState } from "./lib/useRouteState";

import { AppSidebar } from "./components/layout/AppSidebar";
import { AppHeader } from "./components/layout/AppHeader";
import { CreateJobModal } from "./components/modals/CreateJobModal";
import { FailedTweetDrawer } from "./components/drawers/FailedTweetDrawer";
import { GlobalLoadingBar } from "./components/common/GlobalLoadingBar";

import { TaskCenterPage } from "./pages/TaskCenterPage";
import { SchedulesPage } from "./pages/SchedulesPage";
import { GalleryPage } from "./pages/GalleryPage";
import { SettingsPage } from "./pages/SettingsPage";

// 单一浅色主题：颜色、圆角、控件高度统一由 token 下发，页面里不再各写一套。
const BRAND = "#0ea5e9";

const antdThemeConfig = {
  algorithm: antdTheme.defaultAlgorithm,
  token: {
    colorPrimary: BRAND,
    colorInfo: BRAND,
    colorLink: BRAND,
    colorBgContainer: "#ffffff",
    colorBgLayout: "#f5f7fa",
    colorBorder: "#e5e9f0",
    colorBorderSecondary: "#eef1f6",
    colorTextHeading: "#0f172a",
    colorText: "#334155",
    colorTextSecondary: "#64748b",
    colorTextTertiary: "#94a3b8",
    borderRadius: 8,
    borderRadiusLG: 12,
    fontFamily:
      'Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
  },
  components: {
    Button: { controlHeight: 36, controlHeightSM: 28, fontWeight: 500 },
    Card: { paddingLG: 20 },
    Menu: { itemHeight: 40, itemMarginInline: 0, itemBorderRadius: 8 },
    Table: { headerBg: "#f8fafc", cellPaddingBlock: 12 },
    Statistic: { titleFontSize: 12, contentFontSize: 24 },
  },
};

// 侧边栏的 overview / workbench / tasks 都指向任务调度中心这一个页面。
const taskCenterSections = ["overview", "workbench", "tasks"];

export default function App() {
  const queryClient = useQueryClient();
  const screens = Grid.useBreakpoint();
  const isCompact = !screens.lg;

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

  const { sseConnected } = useDashboardEvents(
    queryClient,
    refreshDashboard,
    isWorkbenchActive,
  );

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
    totalJobsCount: currentStats.total,
    activeJobsCount: currentStats.active,
    schedulesCount: schedules.data?.length ?? 0,
    failedTweetCount,
    storageType: config.data?.storageType || "local",
    storagePath: config.data?.downloadDir || "/downloads",
  };

  return (
    <ConfigProvider theme={antdThemeConfig} locale={zhCN}>
      <div className="app-shell flex h-screen w-screen overflow-hidden">
        {/* 任意接口请求在途时的全局顶部加载进度条 */}
        <GlobalLoadingBar />

        {/* 桌面端常驻侧边栏 */}
        {!isCompact && (
          <AppSidebar
            {...sidebarProps}
            onSectionChange={handleSectionChange}
            onOpenCreateModal={() => openCreateModal()}
            onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
          />
        )}

        {/* 移动端抽屉侧边栏 */}
        {isCompact && (
          <Drawer
            placement="left"
            open={mobileMenuOpen}
            onClose={() => setMobileMenuOpen(false)}
            styles={{ body: { padding: 0 } }}
            size={272}
          >
            <AppSidebar
              {...sidebarProps}
              onSectionChange={(section) => {
                handleSectionChange(section);
                setMobileMenuOpen(false);
              }}
              onOpenCreateModal={() => {
                setMobileMenuOpen(false);
                openCreateModal();
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
            sseConnected={sseConnected}
            activeCount={currentStats.active}
            maxConcurrency={config.data?.maxConcurrency ?? 8}
            refreshPending={manualRefreshPending}
            onRefresh={handleManualRefresh}
            onQuickSubmit={(input) => openCreateModal(input)}
            onToggleMobileMenu={() => setMobileMenuOpen(true)}
            showMenuButton={isCompact}
          />

          {/* 页面主内容滚动容器 */}
          <main className="app-main flex-1 overflow-y-auto">
            <div className="app-content page-stack">
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
                <>
                  <Skeleton active paragraph={{ rows: 4 }} />
                  <Skeleton active paragraph={{ rows: 6 }} />
                </>
              )}

              {/* 视图分发：默认首页即为任务调度中心 */}
              {isTaskCenterActive && !(jobs.isLoading && !jobs.data) && (
                <TaskCenterPage
                  jobs={jobsData}
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
                <Skeleton active title paragraph={{ rows: 10 }} />
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
    </ConfigProvider>
  );
}
