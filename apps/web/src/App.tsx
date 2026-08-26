import {
  CloseCircleOutlined,
  CloudDownloadOutlined,
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
  Avatar,
  Badge,
  Button,
  Card,
  Col,
  Drawer,
  Flex,
  Grid,
  Layout,
  Menu,
  Row,
  Skeleton,
  Space,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import type { MenuProps } from "antd";
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
  type Dashboard,
} from "./lib/api";
import {
  invalidateWorkbenchQueries,
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

const { Sider, Content, Header } = Layout;
const { Text, Title } = Typography;
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
    () => invalidateWorkbenchQueries(queryClient),
    [queryClient],
  );

  const { sseConnected } = useDashboardEvents(queryClient, refreshDashboard, activeSection === "overview");

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
  const config = useQuery({
    queryKey: configQueryRoot,
    queryFn: ({ signal }) => getConfig(signal),
    staleTime: 15_000,
    enabled: activeSection === "settings",
  });
  const schedules = useQuery({
    queryKey: archiveScheduleQueryRoot,
    queryFn: ({ signal }) => getArchiveSchedules(signal),
    staleTime: 15_000,
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

  function handleManualRefresh() {
    setManualRefreshPending(true);
    const task = activeSection === "settings"
      ? queryClient.invalidateQueries({ queryKey: configQueryRoot })
      : refreshDashboard();
    void task.finally(() => setManualRefreshPending(false));
  }

  const menuItems: MenuProps["items"] = [
    { key: "overview", icon: <HomeOutlined />, label: "工作台" },
    { key: "settings", icon: <SettingOutlined />, label: "配置" },
  ];

  const jobsData = jobs.data;
  const dashboardData: Dashboard | undefined = jobsData
    ? {
        jobs: jobsData.items,
        downloads: [],
        failed: [],
        failedTweets: [],
        failedTweetCount: meta.data?.failedTweetCount ?? 0,
        archiveSchedules: schedules.data ?? [],
        pagination: {
          page: jobsData.page,
          pageSize: jobsData.pageSize,
          total: meta.data?.stats.total ?? 0,
          totalPages: meta.data?.stats.total
            ? Math.ceil(meta.data.stats.total / jobsData.pageSize)
            : 0,
        },
        stats: meta.data?.stats ?? { total: 0, active: 0, completed: 0, failed: 0 },
      }
    : undefined;
  const isInitialDashboardLoading = activeSection === "settings"
    ? !config.data && config.isLoading
    : !dashboardData && jobs.isLoading;
  const isInitialDashboardError = activeSection === "settings"
    ? !config.data && config.isError
    : !dashboardData && jobs.isError;

  const navigation = (
    <Menu
      mode={isCompact ? "horizontal" : "inline"}
      selectedKeys={[activeSection]}
      items={menuItems}
      onClick={({ key }) => handleSectionChange(key as SectionKey)}
    />
  );

  return (
    <Layout style={{ minHeight: "100vh" }}>
      {!isCompact ? (
        <Sider width={200} theme="light">
          <Card variant="borderless" size="small">
            <Space size={10}>
              <Avatar shape="square" size={32} src={appIconPath} />
              <Flex vertical>
                <Text strong>open-Xdownload</Text>
                <Text type="secondary">X / Twitter 下载器</Text>
              </Flex>
            </Space>
          </Card>
          {navigation}
        </Sider>
      ) : null}

      <Layout>
        {isCompact ? (
          <Header style={{ height: "auto", padding: 0 }}>
            <Card variant="borderless" size="small">
              <Flex align="center" gap={8} wrap="wrap">
                <Avatar shape="square" size={28} src={appIconPath} />
                <Text strong>open-Xdownload</Text>
                {navigation}
              </Flex>
            </Card>
          </Header>
        ) : null}
        <Content>
          <Flex justify="center" style={{ padding: isCompact ? 12 : 24 }}>
            <Flex vertical gap={16} style={{ width: "100%", maxWidth: 1440 }}>
              {isInitialDashboardLoading ? (
                <DashboardSkeleton />
              ) : isInitialDashboardError ? (
                <Alert
                  type="error"
                  showIcon
                  message="加载失败"
                  description={activeSection === "settings"
                    ? config.error instanceof Error ? config.error.message : "请稍后重试"
                    : jobs.error instanceof Error ? jobs.error.message : "请稍后重试"}
                />
              ) : activeSection === "overview" && dashboardData ? (
                <DashboardContent
                  data={dashboardData}
                  sseConnected={sseConnected}
                  refreshPending={manualRefreshPending}
                  onRefresh={handleManualRefresh}
                  onJobPageChange={handleJobPageChange}
                  onJobPageSizeChange={handleJobPageSizeChange}
                />
              ) : activeSection === "settings" && config.data ? (
                <SettingsPage
                  config={config.data}
                  refreshPending={manualRefreshPending}
                  onRefresh={handleManualRefresh}
                />
              ) : null}
            </Flex>
          </Flex>
        </Content>
      </Layout>
    </Layout>
  );
}

function DashboardContent({
  data,
  onRefresh,
  refreshPending,
  sseConnected,
  onJobPageChange,
  onJobPageSizeChange,
}: {
  data: Dashboard;
  onRefresh: () => void;
  refreshPending: boolean;
  sseConnected: boolean;
  onJobPageChange: (page: number) => void;
  onJobPageSizeChange: (pageSize: number) => void;
}) {
  return (
    <OverviewPage
      data={data}
      onRefresh={onRefresh}
      refreshPending={refreshPending}
      sseConnected={sseConnected}
      onJobPageChange={onJobPageChange}
      onJobPageSizeChange={onJobPageSizeChange}
    />
  );
}

function OverviewPage({
  data,
  onRefresh,
  refreshPending,
  sseConnected,
  onJobPageChange,
  onJobPageSizeChange,
}: {
  data: Dashboard;
  onRefresh: () => void;
  refreshPending: boolean;
  sseConnected: boolean;
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
    <Flex vertical gap={16}>
      <PageHeading
        title="工作台"
        description="管理下载任务与归档计划"
        extra={(
          <Space size={8} wrap>
            {!sseConnected ? (
              <Tag icon={<ExclamationCircleOutlined />} color="warning">
                连接已断开，正在重连
              </Tag>
            ) : null}
            <Tooltip title="刷新">
              <Button icon={<ReloadOutlined />} loading={refreshPending} onClick={onRefresh} />
            </Tooltip>
            <Button
              type="primary"
              icon={<CloudDownloadOutlined />}
              onClick={() => setBatchDrawerOpen(true)}
            >
              批量归档
            </Button>
          </Space>
        )}
      />

      {data.stats ? (
        <StatsSummary
          stats={data.stats}
          failedTweetCount={failedTweetCount}
          onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
        />
      ) : null}

      <Card title="新建下载" extra={<Text type="secondary">粘贴推文链接并解析媒体</Text>}>
        <TweetParser />
      </Card>

      <Row gutter={[16, 16]} align="top">
        <Col xs={24} xl={18}>
          <TaskCenterSections
            data={data}
            failedDrawerOpen={failedDrawerOpen}
            onOpenFailedDrawer={() => setFailedDrawerOpen(true)}
            onCloseFailedDrawer={() => setFailedDrawerOpen(false)}
            onJobPageChange={onJobPageChange}
            onJobPageSizeChange={onJobPageSizeChange}
          />
        </Col>
        <Col xs={24} xl={6}>
          <Card
            title={<Space><SyncOutlined />定时计划</Space>}
            extra={<Badge count={data.archiveSchedules?.length ?? 0} showZero color="blue" />}
          >
            <ArchiveScheduleList schedules={data.archiveSchedules ?? []} />
          </Card>
        </Col>
      </Row>

      <Drawer
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
    </Flex>
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
    <Card
      title={<Space><UnorderedListOutlined />任务列表</Space>}
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
          downloads={data.downloads ?? []}
          failed={data.failed ?? []}
          pagination={data.pagination}
          onPageChange={onJobPageChange}
          onPageSizeChange={onJobPageSizeChange}
      />

      <Drawer
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
    </Card>
  );
}

function SettingsPage({
  config,
  onRefresh,
  refreshPending,
}: {
  config: AppConfig;
  onRefresh: () => void;
  refreshPending: boolean;
}) {
  return (
    <ConfigForm config={config} onRefresh={onRefresh} refreshPending={refreshPending} />
  );
}

function DashboardSkeleton() {
  return (
    <Row gutter={[16, 16]}>
      <Col xs={24} xl={18}>
        <Flex vertical gap={16}>
          <Card>
            <Skeleton active paragraph={{ rows: 4 }} />
          </Card>
          <Card>
            <ListSkeleton rows={4} />
          </Card>
        </Flex>
      </Col>
      <Col xs={24} xl={6}>
        <Card>
          <Skeleton active paragraph={{ rows: 6 }} />
        </Card>
      </Col>
    </Row>
  );
}

function PageHeading({
  description,
  extra,
  title,
}: {
  description: string;
  extra?: React.ReactNode;
  title: string;
}) {
  return (
    <Flex align="center" justify="space-between" gap={16} wrap="wrap">
      <Flex vertical>
        <Title level={3}>{title}</Title>
        <Text type="secondary">{description}</Text>
      </Flex>
      {extra}
    </Flex>
  );
}
