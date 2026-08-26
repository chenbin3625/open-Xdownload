import { useQuery, useQueryClient } from "@tanstack/react-query";
import React, { lazy, Suspense, useCallback, useEffect, useState } from "react";
import OverviewPage from "./pages/OverviewPage";
import {
  configQueryRoot,
  dashboardMetaQueryRoot,
  getConfig,
  getDashboardMeta,
  getJobsPage,
  jobsQueryRoot,
} from "./lib/api";
import {
  invalidateWorkbenchQueries,
  useDashboardEvents,
} from "./lib/useDashboardEvents";
import {
  useRouteState,
  type SectionKey,
} from "./lib/useRouteState";

const SettingsPage = lazy(() => import("./components/settings/SettingsPage"));

const appIconPath = "/icon.svg";

export default function App() {
  const queryClient = useQueryClient();
  const {
    activeSection,
    jobPage,
    jobPageSize,
    handleSectionChange,
    handleJobPageChange,
    handleJobPageSizeChange,
    syncServerPage,
  } = useRouteState();

  const [manualRefreshPending, setManualRefreshPending] = useState(false);
  const refreshWorkbench = useCallback(
    () => invalidateWorkbenchQueries(queryClient),
    [queryClient],
  );

  const { sseConnected } = useDashboardEvents(
    queryClient,
    refreshWorkbench,
    activeSection === "overview",
  );

  const jobs = useQuery({
    queryKey: [...jobsQueryRoot, jobPage, jobPageSize],
    queryFn: ({ signal }) => getJobsPage({ page: jobPage, pageSize: jobPageSize, signal }),
    placeholderData: (previousData) => previousData,
    staleTime: 15_000,
    enabled: activeSection === "overview",
  });
  const meta = useQuery({
    queryKey: dashboardMetaQueryRoot,
    queryFn: ({ signal }) => getDashboardMeta(signal),
    staleTime: 30_000,
    enabled: activeSection === "overview",
  });

  useEffect(() => {
    if (jobs.isPlaceholderData || !meta.data) return;
    const totalPages = meta.data.stats.total > 0
      ? Math.ceil(meta.data.stats.total / jobPageSize)
      : 1;
    if (jobPage > totalPages) {
      syncServerPage(totalPages);
    }
  }, [jobPage, jobPageSize, jobs.isPlaceholderData, meta.data, syncServerPage]);

  useEffect(() => {
    const readyForPrefetch = activeSection === "settings" || jobs.isFetched;
    if (!readyForPrefetch) return;
    const prefetchChunks = () => {
      if (activeSection === "overview") {
        void import("./components/settings/SettingsPage");
        if (!queryClient.getQueryData(configQueryRoot)) {
          void queryClient.prefetchQuery({
            queryKey: configQueryRoot,
            queryFn: ({ signal }) => getConfig(signal),
            staleTime: 15_000,
          });
        }
      } else {
        if (!queryClient.getQueryData([...jobsQueryRoot, jobPage, jobPageSize])) {
          void queryClient.prefetchQuery({
            queryKey: [...jobsQueryRoot, jobPage, jobPageSize],
            queryFn: ({ signal }) => getJobsPage({ page: jobPage, pageSize: jobPageSize, signal }),
            staleTime: 15_000,
          });
        }
        if (!queryClient.getQueryData(dashboardMetaQueryRoot)) {
          void queryClient.prefetchQuery({
            queryKey: dashboardMetaQueryRoot,
            queryFn: ({ signal }) => getDashboardMeta(signal),
            staleTime: 30_000,
          });
        }
      }
    };
    if (typeof window.requestIdleCallback === "function") {
      const idleId = window.requestIdleCallback(prefetchChunks, { timeout: 3000 });
      return () => window.cancelIdleCallback(idleId);
    }
    const timeout = window.setTimeout(prefetchChunks, 1800);
    return () => window.clearTimeout(timeout);
  }, [activeSection, jobs.isFetched, jobPage, jobPageSize, queryClient]);

  const handleManualRefresh = useCallback(() => {
    setManualRefreshPending(true);
    const task =
      activeSection === "settings"
        ? queryClient.invalidateQueries({ queryKey: configQueryRoot })
        : refreshWorkbench();
    void task.finally(() => setManualRefreshPending(false));
  }, [activeSection, queryClient, refreshWorkbench]);

  const currentTitle = activeSection === "settings" ? "配置" : "工作台";
  const currentSubtitle =
    activeSection === "settings" ? "存储、下载与 Cookie 配置" : "任务进度与下载记录一览";
  const jobsData = jobs.data;
  const isInitialJobsLoading = !jobsData && jobs.isLoading;
  const isInitialJobsError = !jobsData && jobs.isError;

  return (
    <div className="app-shell">
      <aside className="app-sider">
        <div className="brand-block">
          <div className="brand-mark" aria-hidden="true">
            <img src={appIconPath} alt="" />
          </div>
          <div className="brand-copy">
            <strong>open-Xdownload</strong>
            <span>X / Twitter 下载器</span>
          </div>
        </div>
        <nav className="app-menu" aria-label="主导航">
          <NavItem
            active={activeSection === "overview"}
            icon={<IconHome />}
            label="工作台"
            onClick={() => handleSectionChange("overview" as SectionKey)}
          />
          <NavItem
            active={activeSection === "settings"}
            icon={<IconSettings />}
            label="配置"
            onClick={() => handleSectionChange("settings")}
          />
        </nav>
      </aside>

      <div className="app-layout">
        <main className="app-content">
          <div className="page-toolbar">
            <div className="page-toolbar-copy">
              <strong className="page-toolbar-title">{currentTitle}</strong>
              <span className="page-toolbar-subtitle">{currentSubtitle}</span>
            </div>
            <div className="page-toolbar-actions">
              {!sseConnected ? (
                <span className="sse-warn">
                  <IconWarning />
                  连接已断开，正在重连
                </span>
              ) : null}
              <button
                type="button"
                className="toolbar-icon-btn"
                aria-label="刷新"
                title="刷新"
                disabled={manualRefreshPending}
                onClick={handleManualRefresh}
              >
                <IconRefresh spinning={manualRefreshPending} />
              </button>
            </div>
          </div>
          {activeSection === "settings" ? (
            <Suspense fallback={<WorkbenchSkeleton />}>
              <SettingsPage />
            </Suspense>
          ) : isInitialJobsLoading ? (
            <WorkbenchSkeleton />
          ) : isInitialJobsError ? (
            <div className="shell-error" role="alert">
              <strong>加载失败</strong>
              <span>{jobs.error instanceof Error ? jobs.error.message : "请稍后重试"}</span>
            </div>
          ) : jobsData ? (
            <OverviewPage
              jobs={jobsData}
              meta={meta.data}
              onJobPageChange={handleJobPageChange}
              onJobPageSizeChange={handleJobPageSizeChange}
            />
          ) : null}
        </main>
      </div>
    </div>
  );
}

function NavItem({
  active,
  icon,
  label,
  onClick,
}: {
  active: boolean;
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="app-menu-item"
      aria-current={active ? "page" : undefined}
      onClick={onClick}
    >
      {icon}
      {label}
    </button>
  );
}

function WorkbenchSkeleton() {
  return (
    <div className="workbench-grid">
      <div className="workbench-main">
        <div className="shell-skeleton-block" />
        <div className="shell-skeleton-block shell-skeleton-block-tall" />
      </div>
      <aside className="workbench-rail">
        <div className="shell-skeleton-block shell-skeleton-block-tall" />
      </aside>
    </div>
  );
}

function IconHome() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
      <path d="M4 11.5 12 5l8 6.5" />
      <path d="M6 10.8V20h12v-9.2" />
    </svg>
  );
}

function IconSettings() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9c.3.6.9 1 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1Z" />
    </svg>
  );
}

function IconRefresh({ spinning }: { spinning?: boolean }) {
  return (
    <svg
      className={spinning ? "icon-spin" : undefined}
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      aria-hidden="true"
    >
      <path d="M21 12a9 9 0 1 1-2.6-6.4" />
      <path d="M21 3v6h-6" />
    </svg>
  );
}

function IconWarning() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
      <path d="M12 9v4" />
      <path d="M12 17h.01" />
      <path d="m10.3 4.2-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.7-2.8l-8-14a2 2 0 0 0-3.4 0Z" />
    </svg>
  );
}
