import {
  CloseCircleOutlined,
  CloudDownloadOutlined,
  DownloadOutlined,
  ExclamationCircleOutlined,
  HomeOutlined,
  ReloadOutlined,
  SettingOutlined,
  SyncOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Badge,
  Button,
  Drawer,
  Flex,
  Grid,
  Layout,
  Menu,
  Skeleton,
  Space,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import type { MenuProps } from "antd";
import React, { useCallback, useEffect, useState } from "react";
import {
  getDashboard,
  type AppConfig,
  type Dashboard,
} from "./lib/api";
import {
  dashboardQueryRoot,
  useDashboardEvents,
} from "./lib/useDashboardEvents";
import {
  useRouteState,
  type SectionKey,
} from "./lib/useRouteState";
import { ListSkeleton } from "./components/common/CommonUI";
import { StatsSummary } from "./components/workbench/StatsSummary";
import { TweetParser } from "./components/workbench/TweetParser";
import { BatchDownloadLauncher } from "./components/workbench/BatchDownloadLauncher";
import { ArchiveScheduleList } from "./components/workbench/ArchiveScheduleList";
import { JobTable } from "./components/jobs/JobTable";
import { FailedTweetQueue } from "./components/jobs/FailedTweetQueue";
import { ConfigForm } from "./components/settings/ConfigForm";

const { Sider, Content } = Layout;
const { Text } = Typography;
const appIconPath = "/icon.svg";

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

  const [manualRefreshPending, setManualRefreshPending] = useState(false);
  const refreshDashboard = useCallback(
    () => queryClient.invalidateQueries({ queryKey: dashboardQueryRoot }),
    [queryClient],
  );

  const { sseConnected } = useDashboardEvents(queryClient, refreshDashboard);

  const dashboard = useQuery({
    queryKey: ["dashboard", jobPage, jobPageSize],
    queryFn: () => getDashboard({ page: jobPage, pageSize: jobPageSize }),
    placeholderData: (previousData) => previousData,
  });

  useEffect(() => {
    if (!dashboard.isPlaceholderData) {
      syncServerPage(dashboard.data?.pagination.page);
    }
  }, [dashboard.data?.pagination.page, dashboard.isPlaceholderData, syncServerPage]);

  function handleManualRefresh() {
    setManualRefreshPending(true);
    void refreshDashboard().finally(() => setManualRefreshPending(false));
  }

  const menuItems: MenuProps["items"] = [
    { key: "overview", icon: <HomeOutlined />, label: "工作台" },
    { key: "settings", icon: <SettingOutlined />, label: "配置" },
  ];

  const currentTitle = {
    overview: "工作台",
    settings: "配置",
  }[activeSection];
  const currentSubtitle = {
    overview: "任务进度与下载记录一览",
    settings: "存储、下载与 Cookie 配置",
  }[activeSection];
  const dashboardData = dashboard.data;
  const isInitialDashboardLoading = !dashboardData && dashboard.isLoading;
  const isInitialDashboardError = !dashboardData && dashboard.isError;

  return (
    <Layout className="app-shell">
      <Sider
        className="app-sider"
        width={192}
        theme="light"
        collapsible={false}
        breakpoint="lg"
      >
        <div className="brand-block">
          <div className="brand-mark" aria-hidden="true">
            <img src={appIconPath} alt="" />
          </div>
          <div className="brand-copy">
            <strong>open-Xdownload</strong>
            <span>X / Twitter 下载器</span>
          </div>
        </div>
        <Menu
          className="app-menu"
          mode={isCompact ? "horizontal" : "inline"}
          selectedKeys={[activeSection]}
          items={menuItems}
          onClick={({ key }) => handleSectionChange(key as SectionKey)}
        />
      </Sider>

      <Layout className="app-layout">
        <Content className="app-content">
          <div className="page-toolbar">
            <div className="page-toolbar-copy">
              <Text strong>{currentTitle}</Text>
              <Text type="secondary">{currentSubtitle}</Text>
            </div>
            <Space size={8} wrap>
              {!sseConnected ? (
                <Tag icon={<ExclamationCircleOutlined />} color="warning">
                  连接已断开，正在重连
                </Tag>
              ) : null}
              <Tooltip title="刷新">
                <Button
                  size="small"
                  icon={<ReloadOutlined />}
                  onClick={handleManualRefresh}
                  loading={manualRefreshPending}
                />
              </Tooltip>
            </Space>
          </div>
          {isInitialDashboardLoading ? (
            <DashboardSkeleton />
          ) : isInitialDashboardError ? (
            <Alert
              type="error"
              showIcon
              message="加载失败"
              description={dashboard.error.message}
            />
          ) : dashboardData ? (
            <DashboardContent
              section={activeSection}
              data={dashboardData}
              onJobPageChange={handleJobPageChange}
              onJobPageSizeChange={handleJobPageSizeChange}
            />
          ) : null}
        </Content>
      </Layout>
    </Layout>
  );
}

function DashboardContent({
  section,
  data,
  onJobPageChange,
  onJobPageSizeChange,
}: {
  section: SectionKey;
  data: Dashboard;
  onJobPageChange: (page: number) => void;
  onJobPageSizeChange: (pageSize: number) => void;
}) {
  if (section === "settings") {
    return <SettingsPage config={data.config} />;
  }

  return (
    <OverviewPage
      data={data}
      onJobPageChange={onJobPageChange}
      onJobPageSizeChange={onJobPageSizeChange}
    />
  );
}

function OverviewPage({
  data,
  onJobPageChange,
  onJobPageSizeChange,
}: {
  data: Dashboard;
  onJobPageChange: (page: number) => void;
  onJobPageSizeChange: (pageSize: number) => void;
}) {
  const screens = Grid.useBreakpoint();
  const [batchDrawerOpen, setBatchDrawerOpen] = useState(false);
  const [failedDrawerOpen, setFailedDrawerOpen] = useState(false);
  const failedTweetCount = data.failedTweetCount ?? 0;

  useEffect(() => {
    if (failedTweetCount === 0) {
      setFailedDrawerOpen(false);
    }
  }, [failedTweetCount]);

  return (
    <div className="workbench-page">
      {data.stats ? (
        <StatsSummary
          stats={data.stats}
          failedTweetCount={failedTweetCount}
          onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
        />
      ) : null}

      <div className="workbench-grid">
        <div className="workbench-main">
          <WorkbenchPanel
            icon={<DownloadOutlined />}
            title="单条解析"
            description="推文媒体"
            extra={
              <Button
                type="primary"
                icon={<CloudDownloadOutlined />}
                onClick={() => setBatchDrawerOpen(true)}
              >
                批量归档
              </Button>
            }
          >
            <TweetParser />
          </WorkbenchPanel>

          <TaskCenterSections
            data={data}
            failedDrawerOpen={failedDrawerOpen}
            onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
            onCloseFailedDrawer={() => setFailedDrawerOpen(false)}
            onJobPageChange={onJobPageChange}
            onJobPageSizeChange={onJobPageSizeChange}
          />
        </div>

        <aside className="workbench-rail">
          <WorkbenchPanel
            compact
            icon={<SyncOutlined />}
            title="定时计划"
            description="自动归档"
            extra={<Badge count={data.archiveSchedules?.length ?? 0} showZero color="#1677ff" />}
          >
            <ArchiveScheduleList schedules={data.archiveSchedules ?? []} />
          </WorkbenchPanel>
        </aside>
      </div>

      <Drawer
        className="app-drawer batch-archive-drawer"
        destroyOnHidden
        open={batchDrawerOpen}
        title={
          <Space>
            <CloudDownloadOutlined />
            批量归档
          </Space>
        }
        size={screens.md ? 920 : "100%"}
        onClose={() => setBatchDrawerOpen(false)}
      >
        <BatchDownloadLauncher />
      </Drawer>
    </div>
  );
}

function TaskCenterSections({
  data,
  failedDrawerOpen,
  onOpenFailedDrawer,
  onCloseFailedDrawer,
  onJobPageChange,
  onJobPageSizeChange,
}: {
  data: Dashboard;
  failedDrawerOpen: boolean;
  onOpenFailedDrawer: () => void;
  onCloseFailedDrawer: () => void;
  onJobPageChange: (page: number) => void;
  onJobPageSizeChange: (pageSize: number) => void;
}) {
  const screens = Grid.useBreakpoint();
  const failedTweetCount = data.failedTweetCount ?? 0;

  return (
    <>
      <SectionBlock
        title={
          <Space>
            <UnorderedListOutlined />
            任务列表
          </Space>
        }
        extra={failedTweetCount > 0 ? (
          <Badge count={failedTweetCount} size="small" overflowCount={999}>
            <Button
              size="small"
              danger
              icon={<CloseCircleOutlined />}
              onClick={onOpenFailedDrawer}
            >
              查看失败项
            </Button>
          </Badge>
        ) : null}
      >
        <JobTable
          jobs={data.jobs}
          downloads={data.downloads}
          failed={data.failed}
          pagination={data.pagination}
          onPageChange={onJobPageChange}
          onPageSizeChange={onJobPageSizeChange}
        />
      </SectionBlock>

      <Drawer
        className="app-drawer failed-tweets-drawer"
        destroyOnHidden
        open={failedDrawerOpen}
        title={
          <Space>
            <CloseCircleOutlined />
            失败推文队列
          </Space>
        }
        size={screens.md ? 760 : "100%"}
        onClose={onCloseFailedDrawer}
      >
        <FailedTweetQueue
          items={data.failedTweets ?? []}
          total={failedTweetCount}
        />
      </Drawer>
    </>
  );
}

function SettingsPage({ config }: { config: AppConfig }) {
  return (
    <div className="settings-page">
      <ConfigForm config={config} />
    </div>
  );
}

function WorkbenchPanel({
  children,
  compact,
  description,
  extra,
  icon,
  title,
}: {
  children: React.ReactNode;
  compact?: boolean;
  description: string;
  extra?: React.ReactNode;
  icon: React.ReactNode;
  title: string;
}) {
  return (
    <section className={compact ? "workbench-panel workbench-panel-compact" : "workbench-panel"}>
      <Flex align="flex-start" justify="space-between" gap={12} wrap="wrap" className="workbench-panel-header">
        <Space align="start" size={10}>
          <span className="workbench-panel-icon">{icon}</span>
          <span className="workbench-panel-title">
            <Text strong>{title}</Text>
            <Text type="secondary">{description}</Text>
          </span>
        </Space>
        {extra}
      </Flex>
      {children}
    </section>
  );
}

function DashboardSkeleton() {
  return (
    <div className="workbench-grid">
      <div className="workbench-main">
        <div className="skeleton-block">
          <Skeleton active paragraph={{ rows: 4 }} />
        </div>
        <div className="skeleton-block">
          <ListSkeleton rows={4} />
        </div>
      </div>
      <aside className="workbench-rail">
        <div className="skeleton-block">
          <Skeleton active paragraph={{ rows: 6 }} />
        </div>
      </aside>
    </div>
  );
}

function SectionBlock({
  children,
  extra,
  title,
}: {
  children: React.ReactNode;
  extra?: React.ReactNode;
  title: React.ReactNode;
}) {
  return (
    <section className="section-block">
      <Flex align="center" justify="space-between" gap={10} className="section-heading">
        <Text strong>{title}</Text>
        {extra}
      </Flex>
      {children}
    </section>
  );
}
