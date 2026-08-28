import { theme as antdTheme, Alert, Badge, Button, ConfigProvider, Drawer, Grid, Skeleton } from "antd";
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
  type AppConfig,
  type DashboardMeta,
  type JobsPage,
} from "./lib/api";
import {
  invalidateWorkbenchQueries,
  useDashboardEvents,
} from "./lib/useDashboardEvents";
import { jobStatusBucket } from "./lib/jobStatus";
import { useRouteState, type SectionKey } from "./lib/useRouteState";
import { useTheme } from "./lib/useTheme";

import { AppSidebar } from "./components/layout/AppSidebar";
import { AppHeader } from "./components/layout/AppHeader";
import { CreateJobModal } from "./components/modals/CreateJobModal";
import { FailedTweetDrawer } from "./components/drawers/FailedTweetDrawer";

import { TaskCenterPage } from "./pages/TaskCenterPage";
import { SchedulesPage } from "./pages/SchedulesPage";
import { GalleryPage } from "./pages/GalleryPage";
import { SettingsPage } from "./pages/SettingsPage";

export default function App() {
  const queryClient = useQueryClient();
  const screens = Grid.useBreakpoint();
  const isCompact = !screens.lg;
  const { theme, isDark, toggleTheme } = useTheme();

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
  const [createInitialKind, setCreateInitialKind] = useState<string>("user");
  const [failedDrawerOpen, setFailedDrawerOpen] = useState(false);
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [manualRefreshPending, setManualRefreshPending] = useState(false);

  const refreshDashboard = useCallback(
    () => invalidateWorkbenchQueries(queryClient),
    [queryClient],
  );

  const isWorkbenchActive =
    activeSection === "overview" ||
    activeSection === "workbench" ||
    activeSection === "tasks" ||
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
    queryFn: ({ signal }) =>
      getJobsPage({ page: jobPage, pageSize: jobPageSize, signal }),
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
    staleTime: 30_000,
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

  function openCreateModal(initial = "", kind = "user") {
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

  // 主题配置
  const antdThemeConfig = {
    algorithm: isDark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
    token: {
      colorPrimary: "#0ea5e9",
      borderRadius: 10,
      colorBgContainer: isDark ? "#0f172a" : "#ffffff",
      colorBgLayout: isDark ? "#020617" : "#f8fafc",
      colorBorder: isDark ? "#1e293b" : "#e2e8f0",
      fontFamily:
        'Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
    },
    components: {
      Button: {
        borderRadius: 10,
        controlHeight: 36,
        controlHeightSM: 30,
        fontSize: 13,
        fontSizeSM: 12,
        fontWeight: 500,
      },
    },
  };

  return (
    <ConfigProvider theme={antdThemeConfig} locale={zhCN}>
      <div className="flex flex-col h-screen w-screen overflow-hidden bg-slate-50 dark:bg-slate-950 text-slate-900 dark:text-slate-100 transition-colors duration-200">
        {/* 顶部通知横幅 / 状态栏提示 (Ant Design + Tailwind) */}
        <div className="h-7 bg-gradient-to-r from-sky-900/60 via-slate-900/80 to-indigo-900/60 border-b border-sky-500/20 px-4 text-[12px] text-sky-200 flex items-center justify-between shrink-0 select-none">
          <div className="flex items-center gap-2">
            <Badge status={sseConnected ? "processing" : "warning"} />
            <span className="font-medium text-slate-200">
              SSE 实时连接{sseConnected ? "正常" : "正在重连"}
            </span>
            <span className="text-slate-500">·</span>
            <span className="text-slate-300 truncate">
              {currentStats.active > 0
                ? `当前正在并发执行 ${currentStats.active} 个媒体下载任务`
                : "下载队列当前就绪空闲"}
            </span>
          </div>
          <div className="flex items-center gap-3">
            <span className="text-slate-400 text-[11px] hidden sm:inline">
              Cookie 账号池: 认证有效
            </span>
            <Button
              type="text"
              size="small"
              onClick={toggleTheme}
              className="!h-5 !px-2 !rounded-full !bg-slate-800/90 hover:!bg-slate-700 !text-slate-300 !text-[11px] !border !border-slate-700/80 !font-medium"
            >
              {isDark ? "☀️ 明亮模式" : "🌙 暗黑模式"}
            </Button>
          </div>
        </div>

        {/* 主布局容器 */}
        <div className="flex flex-1 overflow-hidden">
          {/* 桌面端常驻侧边栏 */}
          {!isCompact && (
            <AppSidebar
              activeSection={activeSection}
              onSectionChange={handleSectionChange}
              onOpenCreateModal={() => openCreateModal()}
              onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
              totalJobsCount={currentStats.total}
              activeJobsCount={currentStats.active}
              schedulesCount={schedules.data?.length ?? 0}
              failedTweetCount={failedTweetCount}
              storageType={config.data?.storageType || "local"}
              storagePath={config.data?.downloadDir || "/downloads"}
            />
          )}

        {/* 移动端抽屉侧边栏 */}
        {isCompact && (
          <Drawer
            placement="left"
            open={mobileMenuOpen}
            onClose={() => setMobileMenuOpen(false)}
            styles={{ body: { padding: 0 } }}
            size={280}
          >
            <AppSidebar
              activeSection={activeSection}
              onSectionChange={(sec) => {
                handleSectionChange(sec);
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
              totalJobsCount={currentStats.total}
              activeJobsCount={currentStats.active}
              schedulesCount={schedules.data?.length ?? 0}
              failedTweetCount={failedTweetCount}
              storageType={config.data?.storageType || "local"}
              storagePath={config.data?.downloadDir || "/downloads"}
            />
          </Drawer>
        )}

        {/* 右侧主视窗内容流 */}
        <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
          {/* 全局顶栏 */}
          <AppHeader
            sseConnected={sseConnected}
            activeCount={currentStats.active}
            maxConcurrency={config.data?.maxConcurrency ?? 8}
            refreshPending={manualRefreshPending}
            onRefresh={handleManualRefresh}
            theme={theme}
            onToggleTheme={toggleTheme}
            onQuickSubmit={(input) => openCreateModal(input)}
            onToggleMobileMenu={() => setMobileMenuOpen(true)}
          />

          {/* 页面主内容滚动容器 */}
          <main className="flex-1 overflow-y-auto p-4 md:p-6 lg:p-8">
            <div className="max-w-7xl mx-auto w-full">
              {/* 异常状态提示 */}
              {isWorkbenchActive && jobs.isError && (
                <div className="mb-4">
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
                </div>
              )}

              {activeSection === "settings" && config.isError && (
                <div className="mb-4">
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
                </div>
              )}

              {/* 初始加载骨架屏 */}
              {isWorkbenchActive && !jobs.data && jobs.isLoading && (
                <div className="space-y-4">
                  <Skeleton active paragraph={{ rows: 4 }} />
                  <Skeleton active paragraph={{ rows: 6 }} />
                </div>
              )}

              {/* 视图分发：默认首页即为任务调度中心 */}
              {(activeSection === "overview" ||
                activeSection === "workbench" ||
                activeSection === "tasks") && (
                <TaskCenterPage
                  jobs={jobsData}
                  failedTweetCount={failedTweetCount}
                  pagination={currentPagination}
                  onPageChange={handleJobPageChange}
                  onPageSizeChange={handleJobPageSizeChange}
                  onOpenCreateModal={() => openCreateModal()}
                  onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
                />
              )}

              {activeSection === "schedules" && (
                <SchedulesPage
                  schedules={schedules.data ?? []}
                  onOpenCreateModal={() => openCreateModal("", "schedule")}
                />
              )}

              {activeSection === "gallery" && (
                <GalleryPage jobs={jobsData} />
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
      </div>

        {/* 统一新建任务/归档模态框 */}
        <CreateJobModal
          open={createModalOpen}
          onClose={() => setCreateModalOpen(false)}
          initialInput={createInitialInput}
          initialKind={createInitialKind as any}
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
