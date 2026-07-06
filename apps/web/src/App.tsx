import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CloudDownloadOutlined,
  CopyOutlined,
  DatabaseOutlined,
  DeleteOutlined,
  DownloadOutlined,
  FileDoneOutlined,
  FileTextOutlined,
  FolderOpenOutlined,
  HomeOutlined,
  KeyOutlined,
  LoadingOutlined,
  PauseCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  RetweetOutlined,
  SafetyCertificateOutlined,
  SettingOutlined,
  StopOutlined,
  SyncOutlined,
  UnorderedListOutlined,
  UserAddOutlined,
  UserOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Avatar,
  Badge,
  Button,
  Card,
  Col,
  Descriptions,
  Empty,
  Flex,
  Form,
  Grid,
  Input,
  InputNumber,
  Layout,
  List,
  Menu,
  notification,
  Pagination,
  Popconfirm,
  Progress,
  Row,
  Segmented,
  Select,
  Skeleton,
  Space,
  Spin,
  Statistic,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Tree,
  Typography,
} from "antd";
import type { MenuProps, TableColumnsType, TabsProps, TreeDataNode } from "antd";
import React, { useEffect, useMemo, useState } from "react";
import {
  AuthCheck,
  AppConfig,
  ArchiveSchedule,
  Dashboard,
  DownloadRecord,
  FailedMedia,
  FailedTweet,
  FileNamingMode,
  Job,
  JobKind,
  JobRequest,
  LocalDirectoryListing,
  StorageTestResult,
  StorageType,
  cancelJob,
  checkAuth,
  clearFailedTweets,
  createArchiveSchedule,
  createJob,
  createJobsBatch,
  deleteArchiveSchedule,
  deleteFailedTweet,
  formatBytes,
  getDashboard,
  listLocalDirectories,
  parseTweetLink,
  retryFailedTweets,
  retryJob,
  runArchiveSchedule,
  testStorage,
  updateArchiveSchedule,
  updateConfig,
} from "./lib/api";

const { Sider, Content } = Layout;
const { Text, Paragraph } = Typography;
const { TextArea } = Input;
const { DirectoryTree } = Tree;
const appIconPath = "/icon.svg";

type SectionKey = "overview" | "jobs" | "settings";
type TextTone = "secondary" | "success" | "warning" | "danger";
type RouteState = {
  section: SectionKey;
  jobPage: number;
  jobPageSize: number;
  shouldReplace: boolean;
};
type BackupCookieRow = {
  authToken: string;
  csrfToken: string;
};
type DirectoryTreeNode = TreeDataNode & {
  key: string;
  path: string;
  children?: DirectoryTreeNode[];
};

const fullWidthStyle: React.CSSProperties = { width: "100%" };
const monoInputStyle: React.CSSProperties = {
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
};
const iconStyles = {
  primary: { color: "#1677ff", fontSize: 18 },
  success: { color: "#389e0d", fontSize: 18 },
  danger: { color: "#cf1322", fontSize: 18 },
} satisfies Record<string, React.CSSProperties>;

const fileNamingOptions: Array<{ value: FileNamingMode; label: string }> = [
  { value: "user_tweet", label: "用户名 + 用户 ID + 推文" },
  { value: "tweet_text", label: "仅推文" },
];

const storageOptions: Array<{ value: StorageType; label: string }> = [
  { value: "local", label: "本地目录" },
  { value: "smb", label: "SMB" },
  { value: "webdav", label: "WebDAV" },
];

const defaultListPageSizeOptions = [5, 10, 20, 50];
const tablePageSizeOptions = [10, 20, 50, 100];
const defaultJobPage = 1;
const defaultJobPageSize = 20;
const cancelableStatuses: Job["status"][] = ["pending", "resolving", "downloading"];
const retryableStatuses: Job["status"][] = ["failed", "canceled", "completed"];
const sectionRoutes = {
  overview: "/overview",
  jobs: "/jobs",
  settings: "/settings",
} satisfies Record<SectionKey, string>;
const routeSections: Record<string, SectionKey> = {
  "/": "overview",
  "/overview": "overview",
  "/jobs": "jobs",
  "/settings": "settings",
};

function normalizePathname(pathname: string) {
  const normalized = pathname.replace(/\/+$/, "");
  return normalized === "" ? "/" : normalized;
}

function parsePositiveInteger(value: string | null, fallback: number) {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : fallback;
}

function parseJobPageSize(value: string | null) {
  const parsed = parsePositiveInteger(value, defaultJobPageSize);
  return tablePageSizeOptions.includes(parsed) ? parsed : defaultJobPageSize;
}

function buildRoutePath(section: SectionKey, jobPage = defaultJobPage, jobPageSize = defaultJobPageSize) {
  const params = new URLSearchParams();
  if (section === "jobs") {
    if (jobPage > defaultJobPage) {
      params.set("page", String(jobPage));
    }
    if (jobPageSize !== defaultJobPageSize) {
      params.set("pageSize", String(jobPageSize));
    }
  }
  const query = params.toString();
  return `${sectionRoutes[section]}${query ? `?${query}` : ""}`;
}

function readRouteState(): RouteState {
  if (typeof window === "undefined") {
    return {
      section: "overview",
      jobPage: defaultJobPage,
      jobPageSize: defaultJobPageSize,
      shouldReplace: false,
    };
  }

  const pathname = normalizePathname(window.location.pathname);
  const section = routeSections[pathname] ?? "overview";
  const params = new URLSearchParams(window.location.search);
  const jobPage = section === "jobs" ? parsePositiveInteger(params.get("page"), defaultJobPage) : defaultJobPage;
  const jobPageSize = section === "jobs" ? parseJobPageSize(params.get("pageSize")) : defaultJobPageSize;
  const canonicalRoute = buildRoutePath(section, jobPage, jobPageSize);
  const currentRoute = `${window.location.pathname}${window.location.search}`;

  return {
    section,
    jobPage,
    jobPageSize,
    shouldReplace: currentRoute !== canonicalRoute,
  };
}

function updateBrowserRoute(
  section: SectionKey,
  jobPage = defaultJobPage,
  jobPageSize = defaultJobPageSize,
  replace = false,
) {
  if (typeof window === "undefined") return;

  const nextRoute = buildRoutePath(section, jobPage, jobPageSize);
  const currentRoute = `${window.location.pathname}${window.location.search}`;
  if (currentRoute === nextRoute) return;

  if (replace) {
    window.history.replaceState(null, "", nextRoute);
    return;
  }
  window.history.pushState(null, "", nextRoute);
}

export default function App() {
  const queryClient = useQueryClient();
  const screens = Grid.useBreakpoint();
  const isCompact = !screens.lg;
  const initialRoute = useMemo(() => readRouteState(), []);
  const [activeSection, setActiveSection] = useState<SectionKey>(initialRoute.section);
  const [jobPage, setJobPage] = useState(initialRoute.jobPage);
  const [jobPageSize, setJobPageSize] = useState(initialRoute.jobPageSize);

  const dashboard = useQuery({
    queryKey: ["dashboard", jobPage, jobPageSize],
    queryFn: () => getDashboard({ page: jobPage, pageSize: jobPageSize }),
    placeholderData: (previousData) => previousData,
  });

  useEffect(() => {
    const events = new EventSource("/api/events");
    let timer: ReturnType<typeof setTimeout> | null = null;
    events.onmessage = () => {
      if (timer) return;
      timer = setTimeout(() => {
        timer = null;
        queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      }, 500);
    };
    return () => {
      events.close();
      if (timer) clearTimeout(timer);
    };
  }, [queryClient]);

  useEffect(() => {
    const page = dashboard.data?.pagination.page;
    if (page && page !== jobPage) {
      setJobPage(page);
      if (activeSection === "jobs") {
        updateBrowserRoute("jobs", page, jobPageSize, true);
      }
    }
  }, [activeSection, dashboard.data?.pagination.page, jobPage, jobPageSize]);

  useEffect(() => {
    if (initialRoute.shouldReplace) {
      updateBrowserRoute(initialRoute.section, initialRoute.jobPage, initialRoute.jobPageSize, true);
    }
  }, [initialRoute]);

  useEffect(() => {
    function handlePopState() {
      const route = readRouteState();
      setActiveSection(route.section);
      setJobPage(route.jobPage);
      setJobPageSize(route.jobPageSize);
    }

    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  function handleSectionChange(section: SectionKey) {
    setActiveSection(section);
    updateBrowserRoute(section, jobPage, jobPageSize);
  }

  function handleJobPageChange(page: number) {
    setJobPage(page);
    if (activeSection === "jobs") {
      updateBrowserRoute("jobs", page, jobPageSize);
    }
  }

  function handleJobPageSizeChange(pageSize: number) {
    setJobPageSize(pageSize);
    setJobPage(1);
    if (activeSection === "jobs") {
      updateBrowserRoute("jobs", defaultJobPage, pageSize);
    }
  }

  const menuItems: MenuProps["items"] = [
    { key: "overview", icon: <HomeOutlined />, label: "工作台" },
    { key: "jobs", icon: <UnorderedListOutlined />, label: "任务中心" },
    { key: "settings", icon: <SettingOutlined />, label: "配置" },
  ];

  const currentTitle = {
    overview: "工作台",
    jobs: "任务中心",
    settings: "配置",
  }[activeSection];

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
            <Space size={10} wrap>
              <Text strong>{currentTitle}</Text>
              <Badge
                status={dashboard.isFetching ? "processing" : "success"}
                text={dashboard.isFetching ? "同步中" : "已连接"}
              />
            </Space>
            <Tooltip title="刷新">
              <Button
                size="small"
                icon={<ReloadOutlined />}
                onClick={() => queryClient.invalidateQueries({ queryKey: ["dashboard"] })}
                loading={dashboard.isFetching}
              />
            </Tooltip>
          </div>
          {dashboard.isLoading ? (
            <DashboardSkeleton />
          ) : dashboard.isError ? (
            <Alert
              type="error"
              showIcon
              message="加载失败"
              description={dashboard.error.message}
            />
          ) : dashboard.data ? (
            <DashboardContent
              section={activeSection}
              data={dashboard.data}
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
  if (section === "jobs") {
    return (
      <JobsPage
        data={data}
        onJobPageChange={onJobPageChange}
        onJobPageSizeChange={onJobPageSizeChange}
      />
    );
  }

  if (section === "settings") {
    return <SettingsPage config={data.config} />;
  }

  return <OverviewPage data={data} />;
}

function OverviewPage({ data }: { data: Dashboard }) {
  return (
    <div className="workbench-page">
      <StatsStrip data={data} />

      <div className="workbench-grid">
        <div className="workbench-main">
          <WorkbenchPanel
            icon={<DownloadOutlined />}
            title="单条解析"
            description="推文媒体"
          >
            <TweetParser />
          </WorkbenchPanel>

          <WorkbenchPanel
            icon={<CloudDownloadOutlined />}
            title="批量归档"
            description="用户、列表、关注关系"
          >
            <BatchDownloadLauncher />
          </WorkbenchPanel>
        </div>

        <aside className="workbench-rail">
          <WorkbenchSummary data={data} />
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
    </div>
  );
}

function JobsPage({
  data,
  onJobPageChange,
  onJobPageSizeChange,
}: {
  data: Dashboard;
  onJobPageChange: (page: number) => void;
  onJobPageSizeChange: (pageSize: number) => void;
}) {
  return (
    <Stack>
      <StatsStrip data={data} />
      <Row gutter={[12, 12]}>
        <Col span={24}>
          <SectionBlock
            title={
              <Space>
                <UnorderedListOutlined />
                任务列表
              </Space>
            }
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
        </Col>
      </Row>
      <SectionBlock
        title={
          <Space>
            <CloseCircleOutlined />
            失败推文队列
          </Space>
        }
        extra={<Tag color={data.failedTweetCount > 0 ? "error" : "default"}>{data.failedTweetCount}</Tag>}
      >
        <FailedTweetQueue
          items={data.failedTweets ?? []}
          total={data.failedTweetCount ?? 0}
        />
      </SectionBlock>
    </Stack>
  );
}

function SettingsPage({ config }: { config: AppConfig }) {
  return (
    <div className="settings-page">
      <ConfigForm config={config} />
    </div>
  );
}

function StatsStrip({ data }: { data: Dashboard }) {
  const stats = data.stats ?? { total: 0, active: 0, completed: 0, failed: 0 };
  const items = [
    { key: "total", title: "总任务", value: stats.total, prefix: <DatabaseOutlined /> },
    { key: "active", title: "进行中", value: stats.active, prefix: <SyncOutlined spin={stats.active > 0} /> },
    {
      key: "completed",
      title: "完成",
      value: stats.completed,
      prefix: <CheckCircleOutlined />,
      valueStyle: { color: "#389e0d" },
    },
    {
      key: "failed",
      title: "失败",
      value: stats.failed,
      prefix: <CloseCircleOutlined />,
      valueStyle: { color: stats.failed > 0 ? "#cf1322" : undefined },
    },
  ];

  return (
    <Row gutter={[12, 12]}>
      {items.map((item) => (
        <Col xs={12} lg={6} key={item.key}>
          <Card className="stats-card" size="small">
            <Statistic
              title={item.title}
              value={item.value}
              prefix={item.prefix}
              valueStyle={item.valueStyle}
            />
          </Card>
        </Col>
      ))}
    </Row>
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

function WorkbenchSummary({ data }: { data: Dashboard }) {
  const stats = data.stats ?? { total: 0, active: 0, completed: 0, failed: 0 };
  const enabledSchedules = (data.archiveSchedules ?? []).filter((schedule) => schedule.enabled).length;
  const failedTweetCount = data.failedTweetCount ?? 0;
  return (
    <section className="workbench-summary">
      <Text strong>运行概览</Text>
      <div className="workbench-summary-list">
        <SummaryMetric
          icon={<SyncOutlined spin={stats.active > 0} />}
          label="当前运行"
          value={`${stats.active} 个任务`}
          meta={stats.active > 0 ? "队列正在处理" : "队列空闲"}
          tone={stats.active > 0 ? "success" : "secondary"}
        />
        <SummaryMetric
          icon={<CloseCircleOutlined />}
          label="失败待处理"
          value={`${stats.failed} 个任务`}
          meta={failedTweetCount > 0 ? `${failedTweetCount} 条失败推文` : "无失败推文积压"}
          tone={stats.failed > 0 || failedTweetCount > 0 ? "danger" : "secondary"}
        />
        <SummaryMetric
          icon={<DatabaseOutlined />}
          label="存储位置"
          value={storageTypeLabel(data.config.storageType)}
          meta={storageTargetLabel(data.config)}
        />
        <SummaryMetric
          icon={<SafetyCertificateOutlined />}
          label="账号 Cookie"
          value={hasConfiguredPrimaryCookie(data.config) ? "已配置" : "待配置"}
          meta={data.config.autoFollowProtected ? "保护账号自动关注" : "仅使用现有权限"}
          tone={hasConfiguredPrimaryCookie(data.config) ? "success" : "warning"}
        />
        <SummaryMetric
          icon={<SyncOutlined />}
          label="自动归档"
          value={`${enabledSchedules} 个启用`}
          meta={`共 ${data.archiveSchedules?.length ?? 0} 个计划`}
        />
      </div>
    </section>
  );
}

function SummaryMetric({
  icon,
  label,
  meta,
  tone,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  meta: string;
  tone?: TextTone;
  value: string;
}) {
  return (
    <div className="workbench-summary-item">
      <span className="workbench-summary-icon">{icon}</span>
      <span className="workbench-summary-copy">
        <Text type="secondary">{label}</Text>
        <Text strong type={tone}>
          {value}
        </Text>
        <EllipsisText type="secondary" title={meta}>
          {meta}
        </EllipsisText>
      </span>
    </div>
  );
}

function DashboardSkeleton() {
  return (
    <Stack>
      <Row gutter={[12, 12]}>
        {[0, 1, 2, 3].map((item) => (
          <Col xs={12} lg={6} key={item}>
            <Card size="small">
              <Skeleton active title={{ width: 92 }} paragraph={{ rows: 1, width: 64 }} />
            </Card>
          </Col>
        ))}
      </Row>
      <Row gutter={[12, 12]}>
        <Col xs={24} xl={14}>
          <div className="skeleton-block">
            <Skeleton active paragraph={{ rows: 7 }} />
          </div>
        </Col>
        <Col xs={24} xl={10}>
          <div className="skeleton-block">
            <Skeleton active paragraph={{ rows: 7 }} />
          </div>
        </Col>
      </Row>
      <div className="skeleton-block">
        <ListSkeleton rows={4} />
      </div>
    </Stack>
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

function AppEmpty({ description }: { description: string }) {
  return <Empty style={{ margin: "8px 0" }} image={Empty.PRESENTED_IMAGE_SIMPLE} description={description} />;
}

function ListSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <Space direction="vertical" size={10} style={fullWidthStyle}>
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton
          active
          avatar
          key={index}
          paragraph={{ rows: 2 }}
          title={{ width: index % 2 === 0 ? "42%" : "58%" }}
        />
      ))}
    </Space>
  );
}

function LoadingSurface({
  children,
  loading,
  tip = "加载中",
}: {
  children: React.ReactNode;
  loading?: boolean;
  tip?: string;
}) {
  return (
    <Spin spinning={!!loading} tip={loading ? tip : undefined}>
      <div style={{ minWidth: 0 }}>{children}</div>
    </Spin>
  );
}

function Stack({
  children,
  size = 12,
  style,
}: {
  children: React.ReactNode;
  size?: number;
  style?: React.CSSProperties;
}) {
  return (
    <Space direction="vertical" size={size} style={{ ...fullWidthStyle, ...style }}>
      {children}
    </Space>
  );
}

function Toolbar({ children }: { children: React.ReactNode }) {
  return (
    <Flex align="center" justify="space-between" gap={10} wrap="wrap" style={fullWidthStyle}>
      {children}
    </Flex>
  );
}

function EllipsisText({
  children,
  code,
  style,
  title,
  type,
}: {
  children: React.ReactNode;
  code?: boolean;
  style?: React.CSSProperties;
  title?: string;
  type?: TextTone;
}) {
  return (
    <Text
      code={code}
      type={type}
      title={title}
      style={{
        display: "block",
        maxWidth: "100%",
        overflow: "hidden",
        textOverflow: "ellipsis",
        whiteSpace: "nowrap",
        ...style,
      }}
    >
      {children}
    </Text>
  );
}

function useClientPagination<TItem>(items: TItem[], initialPageSize = 5) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(initialPageSize);
  const total = items.length;
  const maxPage = Math.max(1, Math.ceil(total / pageSize));
  const currentPage = Math.min(page, maxPage);

  useEffect(() => {
    setPage((current) => Math.min(current, maxPage));
  }, [maxPage]);

  const start = (currentPage - 1) * pageSize;
  const pagedItems = items.slice(start, start + pageSize);

  function handleChange(nextPage: number, nextPageSize?: number) {
    if (nextPageSize && nextPageSize !== pageSize) {
      setPageSize(nextPageSize);
      setPage(1);
      return;
    }
    setPage(nextPage);
  }

  return {
    items: pagedItems,
    page: currentPage,
    pageSize,
    total,
    onChange: handleChange,
  };
}

function AppPagination({
  current,
  itemName,
  onChange,
  pageSize,
  pageSizeOptions = defaultListPageSizeOptions,
  total,
}: {
  current: number;
  itemName: string;
  onChange: (page: number, pageSize: number) => void;
  pageSize: number;
  pageSizeOptions?: number[];
  total: number;
}) {
  return (
    <Flex justify="flex-end" style={fullWidthStyle}>
      <Pagination
        current={current}
        disabled={total === 0}
        pageSize={pageSize}
        pageSizeOptions={pageSizeOptions.map(String)}
        showSizeChanger
        showTotal={(totalCount, range) =>
          totalCount > 0
            ? `共 ${totalCount} ${itemName}，当前 ${range[0]}-${range[1]}`
            : `共 0 ${itemName}`
        }
        size="small"
        total={total}
        onChange={onChange}
      />
    </Flex>
  );
}

function PaginatedList<TItem,>({
  bordered,
  emptyDescription,
  itemName,
  items,
  loading,
  maxHeight,
  pageSize = 5,
  renderItem,
  skeletonRows = pageSize,
  size = "default",
}: {
  bordered?: boolean;
  emptyDescription: string;
  itemName: string;
  items: TItem[];
  loading?: boolean;
  maxHeight?: number;
  pageSize?: number;
  renderItem: (item: TItem) => React.ReactNode;
  skeletonRows?: number;
  size?: "small" | "default" | "large";
}) {
  const pagination = useClientPagination(items, pageSize);
  const listStyle = maxHeight ? { maxHeight, overflow: "auto" } : undefined;

  if (loading && items.length === 0) {
    return <ListSkeleton rows={skeletonRows} />;
  }

  if (items.length === 0) {
    return (
      <Stack size={8}>
        <AppEmpty description={emptyDescription} />
        <AppPagination
          current={pagination.page}
          itemName={itemName}
          pageSize={pagination.pageSize}
          total={pagination.total}
          onChange={pagination.onChange}
        />
      </Stack>
    );
  }

  return (
    <LoadingSurface loading={loading}>
      <Stack size={8}>
        <List
          bordered={bordered}
          dataSource={pagination.items}
          locale={{ emptyText: <AppEmpty description={emptyDescription} /> }}
          renderItem={renderItem}
          size={size}
          style={listStyle}
        />
        <AppPagination
          current={pagination.page}
          itemName={itemName}
          pageSize={pagination.pageSize}
          total={pagination.total}
          onChange={pagination.onChange}
        />
      </Stack>
    </LoadingSurface>
  );
}

function TweetParser() {
  const queryClient = useQueryClient();
  const [url, setUrl] = useState("");
  const [parsed, setParsed] = useState<Awaited<ReturnType<typeof parseTweetLink>> | null>(null);
  const parsedHasMedia = parsed !== null && parsed.media.length > 0;

  const parseMutation = useMutation({
    mutationFn: (targetUrl: string) => parseTweetLink(targetUrl),
    onSuccess: (data) => {
      setParsed(data);
      notification.success({
        message: "解析完成",
        description: `发现 ${data.media.length} 个媒体`,
      });
    },
    onError: (error) => {
      notification.error({
        message: "解析失败",
        description: getErrorMessage(error),
      });
    },
  });

  const jobMutation = useMutation({
    mutationFn: () => createJob("tweet_link", url.trim(), parsed?.id ? `Tweet ${parsed.id}` : "推文任务"),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "下载任务已创建" });
    },
    onError: (error) => {
      notification.error({
        message: "创建失败",
        description: getErrorMessage(error),
      });
    },
  });

  function handleParse(targetUrl = url) {
    const trimmed = targetUrl.trim();
    if (!trimmed) return;
    setUrl(trimmed);
    parseMutation.mutate(trimmed);
  }

  return (
    <Stack size={16}>
      <Input.Search
        size="large"
        value={url}
        placeholder="https://x.com/user/status/123"
        enterButton="解析"
        loading={parseMutation.isPending}
        onChange={(event) => {
          setUrl(event.target.value);
          setParsed(null);
        }}
        onSearch={handleParse}
      />

      {parseMutation.isPending && !parsed ? <ListSkeleton rows={2} /> : null}

      {parsed ? (
        <Stack size={10}>
          <Descriptions
            size="small"
            column={{ xs: 1, md: 3 }}
            items={[
              { key: "author", label: "作者", children: `@${parsed.author.screenName || "unknown"}` },
              { key: "tweet", label: "推文", children: parsed.id },
              {
                key: "url",
                label: "链接",
                children: <CopyButton value={parsed.url} label="复制链接" />,
              },
            ]}
          />
          <Paragraph
            style={{
              marginBottom: 0,
              lineHeight: 1.6,
              overflowWrap: "anywhere",
              whiteSpace: "pre-wrap",
            }}
          >
            {parsed.text || "无正文"}
          </Paragraph>
          <MediaList media={parsed.media} />
          <Flex justify="flex-end">
            <Button
              type="primary"
              icon={<DownloadOutlined />}
              loading={jobMutation.isPending}
              disabled={!parsedHasMedia}
              onClick={() => jobMutation.mutate()}
            >
              下载媒体
            </Button>
          </Flex>
        </Stack>
      ) : null}
    </Stack>
  );
}

function MediaList({ media }: { media: Awaited<ReturnType<typeof parseTweetLink>>["media"] }) {
  return (
    <PaginatedList
      size="small"
      bordered
      emptyDescription="未发现可下载媒体"
      itemName="个媒体"
      items={media}
      pageSize={5}
      renderItem={(item) => {
        const mediaUrl = item.bestUrl || item.url;
        return (
          <List.Item actions={[<CopyButton key="copy" value={mediaUrl} label="复制媒体地址" />]}>
            <List.Item.Meta
              avatar={<FileTextOutlined style={iconStyles.primary} />}
              title={<Tag>{mediaTypeLabel(item.type)}</Tag>}
              description={
                <EllipsisText code title={mediaUrl}>
                  {mediaUrl}
                </EllipsisText>
              }
            />
          </List.Item>
        );
      }}
    />
  );
}

function BatchDownloadLauncher() {
  const queryClient = useQueryClient();
  const [users, setUsers] = useState("");
  const [lists, setLists] = useState("");
  const [following, setFollowing] = useState("");
  const [scheduleName, setScheduleName] = useState("");
  const [intervalMinutes, setIntervalMinutes] = useState(360);
  const items = useMemo(() => buildBatchDownloadItems(users, lists, following), [users, lists, following]);

  const createJobs = useMutation({
    mutationFn: () => createJobsBatch({ items }),
    onSuccess: (data) => {
      setUsers("");
      setLists("");
      setFollowing("");
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({
        message: "批量任务已创建",
        description: `已创建 ${data.length} 个任务`,
      });
    },
    onError: (error) => {
      notification.error({
        message: "创建失败",
        description: getErrorMessage(error),
      });
    },
  });

  const createSchedule = useMutation({
    mutationFn: () =>
      createArchiveSchedule({
        name: scheduleName.trim() || defaultArchiveScheduleName(items),
        enabled: true,
        intervalMinutes,
        items,
      }),
    onSuccess: (schedule) => {
      setScheduleName("");
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({
        message: "定时计划已保存",
        description: `${schedule.name} · ${formatIntervalMinutes(schedule.intervalMinutes)}`,
      });
    },
    onError: (error) => {
      notification.error({
        message: "保存失败",
        description: getErrorMessage(error),
      });
    },
  });

  const tabs: TabsProps["items"] = [
    {
      key: "users",
      label: (
        <Space>
          <UserOutlined />
          用户
        </Space>
      ),
      children: (
        <BatchTargetInput
          value={users}
          onChange={setUsers}
          placeholder={"elonmusk\n1234567"}
        />
      ),
    },
    {
      key: "lists",
      label: (
        <Space>
          <UnorderedListOutlined />
          列表
        </Space>
      ),
      children: (
        <BatchTargetInput
          value={lists}
          onChange={setLists}
          placeholder="8901234"
        />
      ),
    },
    {
      key: "following",
      label: (
        <Space>
          <UserAddOutlined />
          关注
        </Space>
      ),
      children: (
        <BatchTargetInput
          value={following}
          onChange={setFollowing}
          placeholder={"567890\n@screen_name"}
        />
      ),
    },
  ];

  const previewSummary = items.length > 0 ? `准备创建 ${items.length} 个任务` : "输入目标后生成预览";

  return (
    <Stack size={14}>
      <Toolbar>
        <Space size={8} wrap>
          <Text type="secondary">待创建</Text>
          <Badge count={items.length} showZero color="#1677ff" />
        </Space>
        <Space size={8} wrap>
          <Input
            value={scheduleName}
            onChange={(event) => setScheduleName(event.target.value)}
            placeholder="计划名称"
            style={{ width: 180 }}
          />
          <InputNumber
            min={5}
            max={43200}
            addonBefore="每"
            addonAfter="分钟"
            value={intervalMinutes}
            onChange={(value) => setIntervalMinutes(value ?? 5)}
            style={{ width: 170 }}
          />
          <Button
            icon={<SyncOutlined />}
            loading={createSchedule.isPending}
            disabled={items.length === 0}
            onClick={() => createSchedule.mutate()}
          >
            保存计划
          </Button>
          <Button
            type="primary"
            icon={<CloudDownloadOutlined />}
            loading={createJobs.isPending}
            disabled={items.length === 0}
            onClick={() => createJobs.mutate()}
          >
            批量下载
          </Button>
        </Space>
      </Toolbar>

      <Row gutter={[16, 16]} align="stretch" className="batch-launcher-grid">
        <Col xs={24} lg={14}>
          <div className="batch-input-pane">
            <Tabs className="batch-target-tabs" items={tabs} />
          </div>
        </Col>
        <Col xs={24} lg={10}>
          <div className="batch-preview-pane">
            <Flex align="center" justify="space-between" gap={10} wrap="wrap" className="batch-preview-heading">
              <Text strong>任务预览</Text>
              <Text type="secondary">{previewSummary}</Text>
            </Flex>
            <PaginatedList
              bordered
              emptyDescription="暂无待创建任务"
              itemName="个任务"
              items={items}
              loading={createJobs.isPending || createSchedule.isPending}
              maxHeight={322}
              pageSize={6}
              renderItem={(item) => (
                <List.Item>
                  <List.Item.Meta
                    title={<Tag>{kindLabel(item.kind)}</Tag>}
                    description={
                      <EllipsisText title={item.input}>
                        {item.input}
                      </EllipsisText>
                    }
                  />
                </List.Item>
              )}
              size="small"
            />
          </div>
        </Col>
      </Row>

    </Stack>
  );
}

function ArchiveScheduleList({ schedules }: { schedules: ArchiveSchedule[] }) {
  const queryClient = useQueryClient();
  const runSchedule = useMutation({
    mutationFn: runArchiveSchedule,
    onSuccess: (jobs) => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({
        message: "计划已开始运行",
        description: `已创建 ${jobs.length} 个任务`,
      });
    },
    onError: (error) => {
      notification.error({
        message: "运行失败",
        description: getErrorMessage(error),
      });
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
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "计划已更新" });
    },
    onError: (error) => {
      notification.error({
        message: "更新失败",
        description: getErrorMessage(error),
      });
    },
  });
  const removeSchedule = useMutation({
    mutationFn: deleteArchiveSchedule,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "计划已删除" });
    },
    onError: (error) => {
      notification.error({
        message: "删除失败",
        description: getErrorMessage(error),
      });
    },
  });

  return (
    <Stack size={8}>
      <PaginatedList
        bordered
        emptyDescription="暂无定时计划"
        itemName="个计划"
        items={schedules}
        pageSize={5}
        renderItem={(schedule) => (
          <List.Item
            actions={[
              <Switch
                key="enabled"
                size="small"
                checked={schedule.enabled}
                loading={toggleSchedule.isPending && toggleSchedule.variables?.schedule.id === schedule.id}
                onChange={(enabled) => toggleSchedule.mutate({ schedule, enabled })}
              />,
              <Tooltip key="run" title="立即运行">
                <Button
                  size="small"
                  type="text"
                  icon={<CloudDownloadOutlined />}
                  loading={runSchedule.isPending && runSchedule.variables === schedule.id}
                  onClick={() => runSchedule.mutate(schedule.id)}
                />
              </Tooltip>,
              <Popconfirm
                key="delete"
                title="删除定时计划"
                description="确认删除这个定时计划？"
                okText="删除"
                cancelText="取消"
                onConfirm={() => removeSchedule.mutate(schedule.id)}
              >
                <Button
                  size="small"
                  danger
                  type="text"
                  icon={<DeleteOutlined />}
                  loading={removeSchedule.isPending && removeSchedule.variables === schedule.id}
                />
              </Popconfirm>,
            ]}
          >
            <List.Item.Meta
              avatar={<SyncOutlined spin={schedule.enabled} style={iconStyles.primary} />}
              title={
                <Space size={8} wrap>
                  <Text strong>{schedule.name}</Text>
                  <Tag color={schedule.enabled ? "processing" : "default"}>
                    {schedule.enabled ? "启用" : "停用"}
                  </Tag>
                  <Tag>{formatIntervalMinutes(schedule.intervalMinutes)}</Tag>
                </Space>
              }
              description={
                <Stack size={4}>
                  <Space size={10} wrap>
                    <Text type="secondary">目标 {schedule.items.length}</Text>
                    <Text type="secondary">下次 {formatDateTime(schedule.nextRunAt)}</Text>
                    <Text type="secondary">上次 {schedule.lastRunAt ? formatDateTime(schedule.lastRunAt) : "未运行"}</Text>
                  </Space>
                  <EllipsisText title={schedule.items.map((item) => `${kindLabel(item.kind)} ${item.input}`).join("，")}>
                    {schedule.items.map((item) => `${kindLabel(item.kind)} ${item.input}`).join("，") || "无目标"}
                  </EllipsisText>
                </Stack>
              }
            />
          </List.Item>
        )}
        size="small"
      />
    </Stack>
  );
}

function BatchTargetInput({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
}) {
  return (
    <TextArea
      value={value}
      onChange={(event) => onChange(event.target.value)}
      placeholder={placeholder}
      autoSize={{ minRows: 6, maxRows: 12 }}
      style={monoInputStyle}
    />
  );
}

function FailedTweetQueue({
  items,
  total,
}: {
  items: FailedTweet[];
  total: number;
}) {
  const queryClient = useQueryClient();
  const retryAll = useMutation({
    mutationFn: retryFailedTweets,
    onSuccess: (job) => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({
        message: "失败推文已加入重试",
        description: job.title || "已创建重试任务",
      });
    },
    onError: (error) => {
      notification.error({
        message: "重试失败",
        description: getErrorMessage(error),
      });
    },
  });
  const removeOne = useMutation({
    mutationFn: deleteFailedTweet,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "失败记录已删除" });
    },
    onError: (error) => {
      notification.error({
        message: "删除失败",
        description: getErrorMessage(error),
      });
    },
  });
  const clearAll = useMutation({
    mutationFn: clearFailedTweets,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "失败队列已清空" });
    },
    onError: (error) => {
      notification.error({
        message: "清空失败",
        description: getErrorMessage(error),
      });
    },
  });

  return (
    <Stack size={10}>
      <Toolbar>
        <Text type="secondary">{total > 0 ? `共 ${total} 条失败记录` : "暂无失败记录"}</Text>
        <Space size={8} wrap>
          <Button
            size="small"
            icon={<RetweetOutlined />}
            loading={retryAll.isPending}
            disabled={total === 0}
            onClick={() => retryAll.mutate()}
          >
            全部重试
          </Button>
          <Popconfirm
            title="清空失败队列"
            description="确认删除全部失败推文记录？"
            okText="清空"
            cancelText="取消"
            disabled={total === 0}
            onConfirm={() => clearAll.mutate()}
          >
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              loading={clearAll.isPending}
              disabled={total === 0}
            >
              清空
            </Button>
          </Popconfirm>
        </Space>
      </Toolbar>

      <PaginatedList
        bordered
        emptyDescription="暂无失败推文"
        itemName="条记录"
        items={items}
        pageSize={6}
        renderItem={(item) => (
          <List.Item
            actions={[
              <CopyButton key="copy" value={item.tweetId} label="复制推文 ID" />,
              <Popconfirm
                key="delete"
                title="删除失败记录"
                description="确认删除这条失败记录？"
                okText="删除"
                cancelText="取消"
                onConfirm={() => removeOne.mutate(item.id)}
              >
                <Button
                  size="small"
                  danger
                  type="text"
                  icon={<DeleteOutlined />}
                  loading={removeOne.isPending && removeOne.variables === item.id}
                />
              </Popconfirm>,
            ]}
          >
            <List.Item.Meta
              avatar={<CloseCircleOutlined style={iconStyles.danger} />}
              title={
                <Space size={8} wrap>
                  <Text strong>{item.jobTitle || item.tweetId}</Text>
                  <Tag>{item.userScreenName ? `@${item.userScreenName}` : item.userId || "未知用户"}</Tag>
                </Space>
              }
              description={
                <Stack size={4}>
                  <EllipsisText type="danger" title={item.error}>
                    {item.error || "未知错误"}
                  </EllipsisText>
                  <Space size={10} wrap>
                    <Text type="secondary">推文 {item.tweetId}</Text>
                    <Text type="secondary">{formatDateTime(item.updatedAt || item.createdAt)}</Text>
                    {item.entityName ? <Text type="secondary">{item.entityName}</Text> : null}
                  </Space>
                </Stack>
              }
            />
          </List.Item>
        )}
        size="small"
      />
    </Stack>
  );
}

function JobTable({
  jobs,
  downloads,
  failed,
  pagination,
  onPageChange,
  onPageSizeChange,
}: {
  jobs: Job[];
  downloads: DownloadRecord[];
  failed: FailedMedia[];
  pagination: Dashboard["pagination"];
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
}) {
  const queryClient = useQueryClient();
  const [expandedJobIds, setExpandedJobIds] = useState<React.Key[]>([]);
  const downloadsByJob = useMemo(() => groupDownloadsByJob(downloads), [downloads]);
  const failedByJob = useMemo(() => groupFailedMediaByJob(failed), [failed]);

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "任务已重新执行" });
    },
    onError: (error) => {
      notification.error({
        message: "重试失败",
        description: getErrorMessage(error),
      });
    },
  });
  const cancel = useMutation({
    mutationFn: cancelJob,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "任务已取消" });
    },
    onError: (error) => {
      notification.error({
        message: "取消失败",
        description: getErrorMessage(error),
      });
    },
  });

  const columns: TableColumnsType<Job> = [
    {
      title: "任务",
      dataIndex: "title",
      key: "title",
      minWidth: 280,
      render: (_, job) => (
        <Stack size={2}>
          <Space size={8} wrap>
            <Text strong>{job.title || kindLabel(job.kind)}</Text>
            <Tag>{kindLabel(job.kind)}</Tag>
          </Space>
          <EllipsisText type="secondary" title={job.input}>
            {job.input}
          </EllipsisText>
        </Stack>
      ),
    },
    {
      title: "状态",
      dataIndex: "status",
      key: "status",
      width: 116,
      render: (status: Job["status"]) => <JobStatusTag status={status} />,
    },
    {
      title: "进度",
      dataIndex: "progress",
      key: "progress",
      minWidth: 220,
      render: (_, job) => (
        <Stack size={4}>
          <Progress percent={clampPercent(job.progress)} size="small" status={progressStatus(job)} />
          <EllipsisText type={job.error ? "danger" : "secondary"} title={job.error || job.message}>
            {job.error || job.message || "暂无消息"}
          </EllipsisText>
        </Stack>
      ),
    },
    {
      title: "更新",
      dataIndex: "updatedAt",
      key: "updatedAt",
      width: 132,
      render: (value: string) => <Text type="secondary">{formatDateTime(value)}</Text>,
    },
    {
      title: "操作",
      key: "actions",
      width: 112,
      fixed: "right",
      render: (_, job) => {
        const canCancel = cancelableStatuses.includes(job.status);
        const canRetry = retryableStatuses.includes(job.status);
        const isCanceling = cancel.isPending && cancel.variables === job.id;
        const isRetrying = retry.isPending && retry.variables === job.id;
        return (
          <Space size={4}>
            <Tooltip title={canCancel ? "取消" : "当前状态不能取消"}>
              <Button
                size="small"
                icon={<StopOutlined />}
                disabled={!canCancel}
                loading={isCanceling}
                onClick={() => cancel.mutate(job.id)}
              />
            </Tooltip>
            <Tooltip title={canRetry ? "重新执行" : "运行中任务不能重试"}>
              <Button
                size="small"
                icon={<RetweetOutlined />}
                disabled={!canRetry}
                loading={isRetrying}
                onClick={() => retry.mutate(job.id)}
              />
            </Tooltip>
          </Space>
        );
      },
    },
  ];

  return (
    <Stack size={8}>
      <Table<Job>
        rowKey="id"
        columns={columns}
        dataSource={jobs}
        pagination={false}
        scroll={{ x: 860 }}
        expandable={{
          expandedRowKeys: expandedJobIds,
          onExpandedRowsChange: (keys) => setExpandedJobIds([...keys]),
          expandedRowRender: (job) => (
            <JobDetails
              job={job}
              downloads={downloadsByJob.get(job.id) ?? []}
              failed={failedByJob.get(job.id) ?? []}
            />
          ),
        }}
        locale={{ emptyText: <AppEmpty description="暂无任务" /> }}
      />
      <AppPagination
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
    </Stack>
  );
}

function JobDetails({
  job,
  downloads,
  failed,
}: {
  job: Job;
  downloads: DownloadRecord[];
  failed: FailedMedia[];
}) {
  const fileCount = downloads.length + failed.length;
  return (
    <Stack size={16} style={{ padding: "8px 4px 4px" }}>
      <Descriptions
        size="small"
        column={{ xs: 1, sm: 2, lg: 4 }}
        items={[
          { key: "id", label: "任务 ID", children: `#${job.id}` },
          { key: "created", label: "创建时间", children: formatDateTime(job.createdAt) },
          { key: "updated", label: "更新时间", children: formatDateTime(job.updatedAt) },
          { key: "files", label: "文件数", children: fileCount },
        ]}
      />
      <JobFiles downloads={downloads} failed={failed} />
    </Stack>
  );
}

function JobFiles({ downloads, failed }: { downloads: DownloadRecord[]; failed: FailedMedia[] }) {
  const total = downloads.length + failed.length;
  if (total === 0) {
    return (
      <PaginatedList
        emptyDescription="暂无文件记录"
        itemName="个文件"
        items={[] as DownloadRecord[]}
        pageSize={5}
        renderItem={() => null}
        size="small"
      />
    );
  }

  const items: TabsProps["items"] = [
    {
      key: "downloads",
      label: `已下载 ${downloads.length}`,
      children: (
        <PaginatedList
          emptyDescription="暂无下载文件"
          itemName="个文件"
          items={downloads}
          pageSize={5}
          renderItem={(item) => (
            <List.Item actions={[<CopyButton key="copy" value={item.filePath} label="复制文件路径" />]}>
              <List.Item.Meta
                avatar={<FileDoneOutlined style={iconStyles.success} />}
                title={
                  <EllipsisText title={item.filePath}>
                    {item.filePath}
                  </EllipsisText>
                }
                description={
                  <Space size={8} wrap>
                    <Text type="secondary">{formatBytes(item.bytes)}</Text>
                    <EllipsisText
                      type="secondary"
                      title={item.mediaUrl}
                      style={{ maxWidth: "min(760px, 58vw)" }}
                    >
                      {item.mediaUrl}
                    </EllipsisText>
                  </Space>
                }
              />
            </List.Item>
          )}
          size="small"
        />
      ),
    },
    {
      key: "failed",
      label: `失败 ${failed.length}`,
      children: (
        <PaginatedList
          emptyDescription="暂无失败媒体"
          itemName="个媒体"
          items={failed}
          pageSize={5}
          renderItem={(item) => (
            <List.Item actions={[<CopyButton key="copy" value={item.mediaUrl} label="复制媒体地址" />]}>
              <List.Item.Meta
                avatar={<CloseCircleOutlined style={iconStyles.danger} />}
                title={
                  <EllipsisText type="danger" title={item.error}>
                    {item.error}
                  </EllipsisText>
                }
                description={
                  <EllipsisText
                    type="secondary"
                    title={item.mediaUrl}
                    style={{ maxWidth: "min(760px, 58vw)" }}
                  >
                    {item.mediaUrl}
                  </EllipsisText>
                }
              />
            </List.Item>
          )}
          size="small"
        />
      ),
    },
  ];

  return <Tabs size="small" items={items} />;
}

function JobStatusTag({ status }: { status: Job["status"] }) {
  const config = {
    pending: { color: "default", icon: <PauseCircleOutlined />, label: "排队" },
    resolving: { color: "processing", icon: <LoadingOutlined spin />, label: "解析" },
    downloading: { color: "blue", icon: <DownloadOutlined />, label: "下载" },
    completed: { color: "success", icon: <CheckCircleOutlined />, label: "完成" },
    failed: { color: "error", icon: <CloseCircleOutlined />, label: "失败" },
    canceled: { color: "default", icon: <StopOutlined />, label: "取消" },
  }[status];
  return (
    <Tag color={config.color} icon={config.icon}>
      {config.label}
    </Tag>
  );
}

function ConfigForm({ config }: { config: AppConfig }) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState(() => normalizeConfig(config));
  const [authResult, setAuthResult] = useState<AuthCheck | null>(null);
  const hasPrimaryCookie = Boolean((draft.authToken ?? "").trim() && (draft.csrfToken ?? "").trim());
  const backupCookieCount = countConfiguredBackupCookies(draft.additionalCookies ?? "");

  useEffect(() => setDraft(normalizeConfig(config)), [config]);

  const mutation = useMutation({
    mutationFn: updateConfig,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notification.success({ message: "配置已保存" });
    },
    onError: (error) => {
      notification.error({
        message: "保存失败",
        description: getErrorMessage(error),
      });
    },
  });

  const authMutation = useMutation({
    mutationFn: checkAuth,
    onSuccess: (result) => {
      setAuthResult(result);
      if (result.ok) {
        notification.success({
          message: "登录校验通过",
          description: result.screenName ? `@${result.screenName}` : result.message,
        });
        return;
      }
      notification.warning({
        message: "登录校验未通过",
        description: result.message,
      });
    },
    onError: (error) => {
      notification.error({
        message: "校验失败",
        description: getErrorMessage(error),
      });
    },
  });

  return (
    <Form layout="vertical" className="config-form">
      <div className="settings-command-bar">
        <div className="settings-heading">
          <Avatar className="settings-heading-icon" icon={<SettingOutlined />} />
          <div className="settings-heading-copy">
            <Text strong>下载配置</Text>
            <Text type="secondary">当前运行参数</Text>
          </div>
        </div>
        <Space wrap className="settings-actions">
          <Button
            icon={<SafetyCertificateOutlined />}
            loading={authMutation.isPending}
            onClick={() => authMutation.mutate()}
          >
            校验登录
          </Button>
          <Button
            type="primary"
            icon={<CheckCircleOutlined />}
            loading={mutation.isPending}
            onClick={() => mutation.mutate(draft)}
          >
            保存配置
          </Button>
        </Space>
      </div>

      <div className="settings-grid">
        <aside className="settings-sidebar">
          <ConfigSummaryRail
            draft={draft}
            authResult={authResult}
            backupCookieCount={backupCookieCount}
            hasPrimaryCookie={hasPrimaryCookie}
          />
        </aside>

        <div className="settings-main">
          <ConfigPanel
            icon={<DatabaseOutlined />}
            title="存储"
          >
            <StorageSettings draft={draft} onChange={setDraft} />
          </ConfigPanel>

          <ConfigPanel
            icon={<DownloadOutlined />}
            title="下载"
          >
            <DownloadSettingsFields draft={draft} onChange={setDraft} />
          </ConfigPanel>

          <ConfigPanel
            icon={<SafetyCertificateOutlined />}
            title="X Cookie"
            extra={<Tag color={hasPrimaryCookie ? "success" : "warning"}>{hasPrimaryCookie ? "已配置" : "待配置"}</Tag>}
          >
            <CookieSettingsFields draft={draft} onChange={setDraft} />
            <Stack size={10}>
              {authResult ? (
                <Alert
                  type={authResult.ok ? "success" : "error"}
                  showIcon
                  message={authResult.ok && authResult.screenName ? `@${authResult.screenName}` : authResult.message}
                />
              ) : null}
              {authResult?.diagnostics ? <CookieDiagnostics diagnostics={authResult.diagnostics} /> : null}
            </Stack>
          </ConfigPanel>
        </div>
      </div>
    </Form>
  );
}

function ConfigPanel({
  children,
  description,
  extra,
  icon,
  title,
}: {
  children: React.ReactNode;
  description?: string;
  extra?: React.ReactNode;
  icon: React.ReactNode;
  title: string;
}) {
  return (
    <section className="settings-panel">
      <Flex align="flex-start" justify="space-between" gap={12} wrap="wrap" className="settings-panel-header">
        <Space align="start" size={10}>
          <span className="settings-panel-icon">{icon}</span>
          <span className="settings-panel-title">
            <Text strong>{title}</Text>
            {description ? <Text type="secondary">{description}</Text> : null}
          </span>
        </Space>
        {extra}
      </Flex>
      {children}
    </section>
  );
}

function ConfigSummaryRail({
  authResult,
  backupCookieCount,
  draft,
  hasPrimaryCookie,
}: {
  authResult: AuthCheck | null;
  backupCookieCount: number;
  draft: AppConfig;
  hasPrimaryCookie: boolean;
}) {
  const authTone = authResult ? (authResult.ok ? "success" : "danger") : hasPrimaryCookie ? "warning" : "secondary";
  const authValue = authResult
    ? authResult.ok
      ? authResult.screenName
        ? `@${authResult.screenName}`
        : "校验通过"
      : "校验异常"
    : hasPrimaryCookie
      ? "等待校验"
      : "未配置";

  return (
    <div className="settings-status-panel">
      <Text strong>配置概览</Text>
      <div className="settings-summary-list">
        <ConfigSummaryItem
          icon={<DatabaseOutlined />}
          label="存储方式"
          value={storageTypeLabel(draft.storageType)}
          meta={storageTargetLabel(draft)}
        />
        <ConfigSummaryItem
          icon={<RetweetOutlined />}
          label="下载策略"
          value={`${draft.maxConcurrency} 并发`}
          meta={draft.autoRetryFailed ? "失败后自动重试" : "失败后保留队列"}
        />
        <ConfigSummaryItem
          icon={<FileDoneOutlined />}
          label="文件命名"
          value={fileNamingModeLabel(draft.fileNamingMode)}
          meta={`最长 ${draft.maxFilenameLength} 字符`}
        />
        <ConfigSummaryItem
          icon={<KeyOutlined />}
          label="账号状态"
          value={authValue}
          meta={backupCookieCount > 0 ? `${backupCookieCount} 组备用 Cookie` : "无备用 Cookie"}
          tone={authTone}
        />
      </div>
    </div>
  );
}

function ConfigSummaryItem({
  icon,
  label,
  meta,
  tone,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  meta: string;
  tone?: TextTone;
  value: string;
}) {
  return (
    <div className="settings-summary-item">
      <span className="settings-summary-icon">{icon}</span>
      <span className="settings-summary-copy">
        <Text type="secondary">{label}</Text>
        <Text strong type={tone}>
          {value}
        </Text>
        <EllipsisText type="secondary" title={meta}>
          {meta}
        </EllipsisText>
      </span>
    </div>
  );
}

function DownloadSettingsFields({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <Row gutter={[16, 0]} className="settings-field-grid">
      <Col xs={24} lg={12}>
        <Form.Item label="代理">
          <Input
            value={draft.proxyUrl}
            onChange={(event) => onChange((current) => ({ ...current, proxyUrl: event.target.value }))}
            placeholder="http://127.0.0.1:7890"
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="并发">
          <InputNumber
            min={1}
            max={64}
            value={draft.maxConcurrency}
            onChange={(value) => onChange((current) => ({ ...current, maxConcurrency: value ?? 1 }))}
            style={fullWidthStyle}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="最大文件名长度">
          <InputNumber
            min={16}
            max={240}
            value={draft.maxFilenameLength}
            onChange={(value) => onChange((current) => ({ ...current, maxFilenameLength: value ?? 120 }))}
            style={fullWidthStyle}
          />
        </Form.Item>
      </Col>
      <Col xs={24} lg={12}>
        <Form.Item label="文件名命名">
          <Select
            value={draft.fileNamingMode}
            options={fileNamingOptions}
            onChange={(value) => onChange((current) => ({ ...current, fileNamingMode: value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="失败重试">
          <Switch
            checked={draft.autoRetryFailed}
            onChange={(checked) => onChange((current) => ({ ...current, autoRetryFailed: checked }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="保护账号自动关注">
          <Switch
            checked={draft.autoFollowProtected}
            onChange={(checked) => onChange((current) => ({ ...current, autoFollowProtected: checked }))}
          />
        </Form.Item>
      </Col>
    </Row>
  );
}

function CookieSettingsFields({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <div className="settings-cookie-fields">
      <Row gutter={[16, 0]} className="settings-field-grid">
        <Col xs={24} lg={12}>
          <Form.Item label="auth_token">
            <Input
              prefix={<KeyOutlined />}
              value={draft.authToken ?? ""}
              onChange={(event) => onChange((current) => ({ ...current, authToken: event.target.value }))}
            />
          </Form.Item>
        </Col>
        <Col xs={24} lg={12}>
          <Form.Item label="ct0">
            <Input
              prefix={<KeyOutlined />}
              value={draft.csrfToken ?? ""}
              onChange={(event) => onChange((current) => ({ ...current, csrfToken: event.target.value }))}
            />
          </Form.Item>
        </Col>
      </Row>
      <BackupCookieInputs
        value={draft.additionalCookies ?? ""}
        onChange={(additionalCookies) => onChange((current) => ({ ...current, additionalCookies }))}
      />
    </div>
  );
}

function CookieDiagnostics({ diagnostics }: { diagnostics: NonNullable<AuthCheck["diagnostics"]> }) {
  return (
    <Stack size={8}>
      <Descriptions
        size="small"
        column={{ xs: 1, sm: 3 }}
        items={[
          { key: "total", label: "账号数", children: diagnostics.total },
          { key: "available", label: "可用", children: diagnostics.available },
          {
            key: "blocked",
            label: "受限",
            children: diagnostics.clients.filter((client) => client.rateLimits.some((limit) => limit.blocked)).length,
          },
        ]}
      />
      <PaginatedList
        bordered
        emptyDescription="暂无账号诊断"
        itemName="个账号"
        items={diagnostics.clients}
        pageSize={5}
        renderItem={(client) => (
          <List.Item>
            <List.Item.Meta
              avatar={<Avatar>{client.index + 1}</Avatar>}
              title={
                <Space size={8} wrap>
                  <Text strong>{client.screenName ? `@${client.screenName}` : `账号 ${client.index + 1}`}</Text>
                  {client.primary ? <Tag color="processing">主账号</Tag> : null}
                  {client.disabled ? <Tag color="default">禁用</Tag> : null}
                  <Tag color={client.ok ? "success" : "error"}>{client.ok ? "可用" : "异常"}</Tag>
                </Space>
              }
              description={
                <Stack size={4}>
                  {client.error ? (
                    <EllipsisText type="danger" title={client.error}>
                      {client.error}
                    </EllipsisText>
                  ) : (
                    <Text type="secondary">请求 {client.requestCount}</Text>
                  )}
                  <Space size={6} wrap>
                    {client.rateLimits.slice(0, 4).map((limit) => (
                      <Tag key={limit.path} color={limit.blocked ? "error" : "default"}>
                        {limit.path} {limit.remaining}/{limit.limit}
                      </Tag>
                    ))}
                  </Space>
                </Stack>
              }
            />
          </List.Item>
        )}
        size="small"
      />
    </Stack>
  );
}

function StorageSettings({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  const [testResult, setTestResult] = useState<StorageTestResult | null>(null);
  const [testError, setTestError] = useState("");
  const testable = draft.storageType === "smb" || draft.storageType === "webdav";
  const storageTestMutation = useMutation({
    mutationFn: testStorage,
    onSuccess: (result) => {
      setTestResult(result);
      setTestError("");
      notification.success({
        message: "存储测试通过",
        description: result.root,
      });
    },
    onError: (error) => {
      const message = getErrorMessage(error);
      setTestResult(null);
      setTestError(message);
      notification.error({
        message: "存储测试失败",
        description: message,
      });
    },
  });

  useEffect(() => {
    setTestResult(null);
    setTestError("");
  }, [draft.storageType]);

  return (
    <Stack size={16}>
      <Segmented
        block
        value={draft.storageType}
        options={storageOptions.map((option) => ({ value: option.value, label: option.label }))}
        onChange={(value) => onChange((current) => ({ ...current, storageType: value as StorageType }))}
      />

      {draft.storageType === "local" ? (
        <LocalDirectoryPicker
          path={draft.downloadDir}
          onSelect={(downloadDir) => onChange((current) => ({ ...current, downloadDir }))}
        />
      ) : null}
      {draft.storageType === "smb" ? <SMBStorageFields draft={draft} onChange={onChange} /> : null}
      {draft.storageType === "webdav" ? <WebDAVStorageFields draft={draft} onChange={onChange} /> : null}
      {testable ? (
        <RemoteStorageTestPanel
          draft={draft}
          error={testError}
          loading={storageTestMutation.isPending}
          result={testResult}
          onTest={() => storageTestMutation.mutate(draft)}
        />
      ) : null}
    </Stack>
  );
}

function RemoteStorageTestPanel({
  draft,
  error,
  loading,
  result,
  onTest,
}: {
  draft: AppConfig;
  error: string;
  loading: boolean;
  result: StorageTestResult | null;
  onTest: () => void;
}) {
  return (
    <Stack size={8} style={{ marginTop: -6 }}>
      <Toolbar>
        <EllipsisText type="secondary" title={storageTargetLabel(draft)} style={{ flex: "1 1 220px", minWidth: 0 }}>
          {storageTargetLabel(draft)}
        </EllipsisText>
        <Button icon={<SafetyCertificateOutlined />} loading={loading} onClick={onTest}>
          测试连接
        </Button>
      </Toolbar>
      {result ? (
        <Alert
          type="success"
          showIcon
          message={result.message}
          description={
            <Stack size={2}>
              <EllipsisText title={result.root}>{result.root}</EllipsisText>
              <EllipsisText type="secondary" title={result.path}>
                {result.path}
              </EllipsisText>
            </Stack>
          }
        />
      ) : null}
      {error ? <Alert type="error" showIcon message="存储测试失败" description={error} /> : null}
    </Stack>
  );
}

function LocalDirectoryPicker({ path, onSelect }: { path: string; onSelect: (path: string) => void }) {
  const [rootPath, setRootPath] = useState(path);
  const [selectedPath, setSelectedPath] = useState(path);
  const [expandedKeys, setExpandedKeys] = useState<React.Key[]>([]);
  const [treeData, setTreeData] = useState<DirectoryTreeNode[]>([]);
  const listing = useQuery<LocalDirectoryListing>({
    queryKey: ["local-directories", rootPath],
    queryFn: () => listLocalDirectories(rootPath),
  });
  const resolvedPath = listing.data?.path ?? rootPath;

  useEffect(() => {
    setRootPath(path);
    setSelectedPath(path);
  }, [path]);

  useEffect(() => {
    if (!listing.data) {
      return;
    }
    const rootNode = listingToDirectoryTreeRoot(listing.data);
    setTreeData([rootNode]);
    setExpandedKeys([rootNode.key]);
    setSelectedPath(rootNode.path);
  }, [listing.data]);

  async function loadDirectoryNode(node: DirectoryTreeNode) {
    if (node.children || node.isLeaf) {
      return;
    }
    try {
      const childListing = await listLocalDirectories(node.path);
      const children = childListing.entries.map(directoryEntryToTreeNode);
      setTreeData((current) => updateDirectoryTreeChildren(current, node.key, children));
    } catch (error) {
      notification.error({
        message: "读取目录失败",
        description: getErrorMessage(error),
      });
    }
  }

  return (
    <Stack size={10}>
      <Space.Compact style={fullWidthStyle}>
        <Input readOnly prefix={<FolderOpenOutlined />} value={selectedPath} />
        <Button type="primary" onClick={() => onSelect(selectedPath)} disabled={!selectedPath}>
          选择此目录
        </Button>
      </Space.Compact>

      <Card size="small">
        <Stack size={8}>
          <Toolbar>
            <Button
              icon={<FolderOpenOutlined />}
              disabled={!listing.data?.parent || listing.isLoading}
              onClick={() => listing.data?.parent && setRootPath(listing.data.parent)}
            >
              上级
            </Button>
            <EllipsisText title={resolvedPath} style={{ flex: "1 1 220px", minWidth: 0 }}>
              {resolvedPath}
            </EllipsisText>
            <Tooltip title="刷新目录">
              <Button
                icon={<ReloadOutlined />}
                loading={listing.isFetching}
                onClick={() => listing.refetch()}
              />
            </Tooltip>
          </Toolbar>

          {listing.isError ? (
            <Alert type="error" showIcon message="读取目录失败" description={listing.error.message} />
          ) : null}

          <Spin spinning={listing.isLoading}>
            {treeData.length > 0 ? (
              <DirectoryTree<DirectoryTreeNode>
                blockNode
                height={280}
                expandAction="doubleClick"
                expandedKeys={expandedKeys}
                loadData={(node) => loadDirectoryNode(node as DirectoryTreeNode)}
                selectedKeys={selectedPath ? [selectedPath] : []}
                treeData={treeData}
                onExpand={(keys) => setExpandedKeys([...keys])}
                onSelect={(_, info) => setSelectedPath((info.node as DirectoryTreeNode).path)}
              />
            ) : (
              <AppEmpty description="没有子目录" />
            )}
          </Spin>
        </Stack>
      </Card>
    </Stack>
  );
}

function SMBStorageFields({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <Row gutter={[16, 0]}>
      <Col xs={24} md={12}>
        <Form.Item label="主机">
          <Input
            value={draft.smbHost}
            onChange={(event) => onChange((current) => ({ ...current, smbHost: event.target.value }))}
            placeholder="192.168.1.10"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="端口">
          <InputNumber
            min={1}
            max={65535}
            value={draft.smbPort}
            onChange={(value) => onChange((current) => ({ ...current, smbPort: value ?? 445 }))}
            style={fullWidthStyle}
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="共享名">
          <Input
            value={draft.smbShare}
            onChange={(event) => onChange((current) => ({ ...current, smbShare: event.target.value }))}
            placeholder="downloads"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="目录">
          <Input
            value={draft.smbPath}
            onChange={(event) => onChange((current) => ({ ...current, smbPath: event.target.value }))}
            placeholder="x-media"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="域">
          <Input
            value={draft.smbDomain}
            onChange={(event) => onChange((current) => ({ ...current, smbDomain: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="用户名">
          <Input
            value={draft.smbUsername}
            onChange={(event) => onChange((current) => ({ ...current, smbUsername: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24}>
        <Form.Item label="密码">
          <Input.Password
            value={draft.smbPassword ?? ""}
            onChange={(event) => onChange((current) => ({ ...current, smbPassword: event.target.value }))}
          />
        </Form.Item>
      </Col>
    </Row>
  );
}

function WebDAVStorageFields({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <Row gutter={[16, 0]}>
      <Col xs={24}>
        <Form.Item label="地址">
          <Input
            value={draft.webdavUrl}
            onChange={(event) => onChange((current) => ({ ...current, webdavUrl: event.target.value }))}
            placeholder="https://example.com/dav"
          />
        </Form.Item>
      </Col>
      <Col xs={24}>
        <Form.Item label="目录">
          <Input
            value={draft.webdavPath}
            onChange={(event) => onChange((current) => ({ ...current, webdavPath: event.target.value }))}
            placeholder="x-media"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="用户名">
          <Input
            value={draft.webdavUsername}
            onChange={(event) => onChange((current) => ({ ...current, webdavUsername: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="密码">
          <Input.Password
            value={draft.webdavPassword ?? ""}
            onChange={(event) => onChange((current) => ({ ...current, webdavPassword: event.target.value }))}
          />
        </Form.Item>
      </Col>
    </Row>
  );
}

function BackupCookieInputs({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const redactedCookieValue = "********";
  const [rows, setRows] = useState<BackupCookieRow[]>(() => parseBackupCookieRows(value));

  useEffect(() => {
    const nextRows = parseBackupCookieRows(value);
    const nextValue = normalizeBackupCookieRows(nextRows);
    setRows((currentRows) =>
      normalizeBackupCookieRows(currentRows) === nextValue ? currentRows : nextRows,
    );
  }, [value]);

  function updateRow(index: number, field: keyof BackupCookieRow, nextValue: string) {
    setRows((currentRows) => {
      const nextRows = currentRows.map((row, rowIndex) => {
        if (rowIndex !== index) {
          return row;
        }
        if (isRedactedBackupCookieRow(row) && nextValue !== redactedCookieValue) {
          return { authToken: field === "authToken" ? nextValue : "", csrfToken: field === "csrfToken" ? nextValue : "" };
        }
        return { ...row, [field]: nextValue };
      });
      onChange(normalizeBackupCookieRows(nextRows));
      return nextRows;
    });
  }

  function addRow() {
    setRows((currentRows) => {
      if (currentRows.length === 1 && isRedactedBackupCookieRow(currentRows[0])) {
        onChange("");
        return [emptyBackupCookieRow()];
      }
      return [...currentRows, emptyBackupCookieRow()];
    });
  }

  function removeRow(index: number) {
    setRows((currentRows) => {
      const nextRows = currentRows.filter((_, rowIndex) => rowIndex !== index);
      const safeRows = nextRows.length > 0 ? nextRows : [emptyBackupCookieRow()];
      onChange(normalizeBackupCookieRows(safeRows));
      return safeRows;
    });
  }

  return (
    <Form.Item
      label={
        <Space>
          <span>备用 Cookie</span>
          <Button size="small" icon={<PlusOutlined />} onClick={addRow} />
        </Space>
      }
    >
      <Stack size={8}>
        {rows.map((row, index) => (
          <Row key={index} gutter={[8, 8]} align="middle">
            <Col xs={24} md={11}>
              <Input
                prefix={<KeyOutlined />}
                value={row.authToken}
                onChange={(event) => updateRow(index, "authToken", event.target.value)}
                placeholder={`备用 ${index + 1} auth_token`}
              />
            </Col>
            <Col xs={24} md={11}>
              <Input
                prefix={<KeyOutlined />}
                value={row.csrfToken}
                onChange={(event) => updateRow(index, "csrfToken", event.target.value)}
                placeholder={`备用 ${index + 1} ct0`}
              />
            </Col>
            <Col xs={24} md={2}>
              <Tooltip title="删除备用 Cookie">
                <Button icon={<DeleteOutlined />} onClick={() => removeRow(index)} />
              </Tooltip>
            </Col>
          </Row>
        ))}
      </Stack>
    </Form.Item>
  );
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);

  async function handleCopy() {
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(value);
      } else {
        const textarea = document.createElement("textarea");
        textarea.value = value;
        textarea.style.position = "fixed";
        textarea.style.opacity = "0";
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        document.body.removeChild(textarea);
      }
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      setCopied(false);
    }
  }

  return (
    <Tooltip title={copied ? "已复制" : label}>
      <Button
        size="small"
        type={copied ? "primary" : "text"}
        icon={copied ? <CheckCircleOutlined /> : <CopyOutlined />}
        onClick={handleCopy}
      />
    </Tooltip>
  );
}

function normalizeConfig(config: AppConfig): AppConfig {
  return {
    ...config,
    storageType: config.storageType ?? "local",
    smbHost: config.smbHost ?? "",
    smbPort: config.smbPort || 445,
    smbShare: config.smbShare ?? "",
    smbPath: config.smbPath ?? "",
    smbDomain: config.smbDomain ?? "",
    smbUsername: config.smbUsername ?? "",
    smbPassword: config.smbPassword ?? "",
    webdavUrl: config.webdavUrl ?? "",
    webdavPath: config.webdavPath ?? "",
    webdavUsername: config.webdavUsername ?? "",
    webdavPassword: config.webdavPassword ?? "",
  };
}

function hasConfiguredPrimaryCookie(config: AppConfig) {
  return Boolean((config.authToken ?? "").trim() && (config.csrfToken ?? "").trim());
}

function emptyBackupCookieRow(): BackupCookieRow {
  return { authToken: "", csrfToken: "" };
}

function parseBackupCookieRows(value: string): BackupCookieRow[] {
  const trimmed = value.trim();
  if (!trimmed) {
    return [emptyBackupCookieRow()];
  }
  if (trimmed === "********") {
    return [{ authToken: "********", csrfToken: "********" }];
  }

  const jsonRows = parseBackupCookieRowsFromJSON(trimmed);
  if (jsonRows.length > 0) {
    return jsonRows;
  }

  const rows: BackupCookieRow[] = [];
  let current = emptyBackupCookieRow();
  const flush = () => {
    if (current.authToken || current.csrfToken) {
      rows.push(current);
    }
    current = emptyBackupCookieRow();
  };

  for (const rawLine of trimmed.split(/\r?\n/)) {
    const line = rawLine.trim().replace(/^-+\s*/, "");
    if (!line) {
      flush();
      continue;
    }
    if (line.includes(":") && !/[;,]/.test(line)) {
      if (setBackupCookieValue(current, line) && current.authToken && current.csrfToken) {
        flush();
      }
      continue;
    }
    for (const token of line.split(/[;,\s]+/)) {
      setBackupCookieValue(current, token);
    }
    if (current.authToken && current.csrfToken) {
      flush();
    }
  }
  flush();
  return rows.length > 0 ? rows : [emptyBackupCookieRow()];
}

function parseBackupCookieRowsFromJSON(value: string): BackupCookieRow[] {
  try {
    const parsed: unknown = JSON.parse(value);
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed.flatMap((item) => {
      if (!item || typeof item !== "object") {
        return [];
      }
      const record = item as Record<string, unknown>;
      const authToken = firstString(record.authToken, record.auth_token);
      const csrfToken = firstString(record.csrfToken, record.ct0);
      return authToken || csrfToken ? [{ authToken, csrfToken }] : [];
    });
  } catch {
    return [];
  }
}

function setBackupCookieValue(current: BackupCookieRow, raw: string) {
  const [key, value] = splitCookieKeyValue(raw);
  if (!key || !value) {
    return false;
  }
  if (key === "auth_token" || key === "authToken") {
    current.authToken = value;
    return true;
  }
  if (key === "ct0" || key === "csrfToken") {
    current.csrfToken = value;
    return true;
  }
  return false;
}

function splitCookieKeyValue(raw: string) {
  const separatorIndex = raw.search(/[=:]/);
  if (separatorIndex < 0) {
    return ["", ""] as const;
  }
  const key = raw.slice(0, separatorIndex).trim();
  const value = raw
    .slice(separatorIndex + 1)
    .trim()
    .replace(/^["']|["']$/g, "");
  return [key, value] as const;
}

function firstString(...values: unknown[]) {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

function normalizeBackupCookieRows(rows: BackupCookieRow[]) {
  if (rows.length === 1 && isRedactedBackupCookieRow(rows[0])) {
    return "********";
  }
  return rows
    .map((row) => ({ authToken: row.authToken.trim(), csrfToken: row.csrfToken.trim() }))
    .map((row) => [
      row.authToken ? `auth_token=${row.authToken}` : "",
      row.csrfToken ? `ct0=${row.csrfToken}` : "",
    ].filter(Boolean).join("; "))
    .filter(Boolean)
    .join("\n");
}

function isRedactedBackupCookieRow(row: BackupCookieRow) {
  return row.authToken === "********" && row.csrfToken === "********";
}

function groupDownloadsByJob(downloads: DownloadRecord[]) {
  const grouped = new Map<number, DownloadRecord[]>();
  for (const item of downloads) {
    const items = grouped.get(item.jobId) ?? [];
    items.push(item);
    grouped.set(item.jobId, items);
  }
  return grouped;
}

function groupFailedMediaByJob(failed: FailedMedia[]) {
  const grouped = new Map<number, FailedMedia[]>();
  for (const item of failed) {
    const items = grouped.get(item.jobId) ?? [];
    items.push(item);
    grouped.set(item.jobId, items);
  }
  return grouped;
}

function listingToDirectoryTreeRoot(listing: LocalDirectoryListing): DirectoryTreeNode {
  return {
    key: listing.path,
    path: listing.path,
    title: listing.path,
    icon: <FolderOpenOutlined />,
    children: listing.entries.map(directoryEntryToTreeNode),
    isLeaf: listing.entries.length === 0,
  };
}

function directoryEntryToTreeNode(entry: LocalDirectoryListing["entries"][number]): DirectoryTreeNode {
  return {
    key: entry.path,
    path: entry.path,
    title: entry.name,
    icon: <FolderOpenOutlined />,
    isLeaf: !entry.hasChildren,
  };
}

function updateDirectoryTreeChildren(
  nodes: DirectoryTreeNode[],
  targetKey: React.Key,
  children: DirectoryTreeNode[],
): DirectoryTreeNode[] {
  return nodes.map((node) => {
    if (node.key === targetKey) {
      return { ...node, children, isLeaf: children.length === 0 };
    }
    if (node.children) {
      return { ...node, children: updateDirectoryTreeChildren(node.children, targetKey, children) };
    }
    return node;
  });
}

function buildBatchDownloadItems(users: string, lists: string, following: string): JobRequest[] {
  const items: JobRequest[] = [];
  for (const input of parseTargets(users)) {
    items.push({ kind: "user", input, title: `用户 ${displayDownloadTarget(input)}` });
  }
  for (const input of parseTargets(lists)) {
    items.push({ kind: "list", input, title: `列表 ${input}` });
  }
  for (const input of parseTargets(following)) {
    items.push({ kind: "following", input, title: `关注 ${displayDownloadTarget(input)}` });
  }
  const seen = new Set<string>();
  return items.filter((item) => {
    const key = `${item.kind}:${item.input.toLowerCase()}`;
    if (seen.has(key)) {
      return false;
    }
    seen.add(key);
    return true;
  });
}

function defaultArchiveScheduleName(items: JobRequest[]) {
  if (items.length === 0) {
    return "批量归档计划";
  }
  const first = items[0];
  return `${kindLabel(first.kind)} ${first.input}${items.length > 1 ? ` 等 ${items.length} 个目标` : ""}`;
}

function parseTargets(value: string) {
  return value
    .split(/[\n,，\s]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function displayDownloadTarget(input: string) {
  if (input.startsWith("@") || /^\d+$/.test(input)) {
    return input;
  }
  return `@${input}`;
}

function formatIntervalMinutes(minutes: number) {
  if (!Number.isFinite(minutes) || minutes <= 0) {
    return "未设置";
  }
  if (minutes % 1440 === 0) {
    const days = minutes / 1440;
    return `每 ${days} 天`;
  }
  if (minutes % 60 === 0) {
    const hours = minutes / 60;
    return `每 ${hours} 小时`;
  }
  return `每 ${minutes} 分钟`;
}

function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function clampPercent(value: number) {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(100, Math.max(0, Math.round(value * 100)));
}

function progressStatus(job: Job) {
  if (job.status === "failed") return "exception";
  if (job.status === "completed") return "success";
  if (job.status === "downloading" || job.status === "resolving") return "active";
  return "normal";
}

function storageTypeLabel(type: StorageType) {
  return storageOptions.find((option) => option.value === type)?.label ?? "本地目录";
}

function storageTargetLabel(config: AppConfig) {
  if (config.storageType === "smb") {
    return config.smbShare ? `//${config.smbHost || "SMB"}/${config.smbShare}` : config.smbHost || "SMB 未配置";
  }
  if (config.storageType === "webdav") {
    return config.webdavUrl || "WebDAV 未配置";
  }
  return config.downloadDir || "本地目录未配置";
}

function fileNamingModeLabel(mode: FileNamingMode) {
  return fileNamingOptions.find((option) => option.value === mode)?.label ?? "仅推文";
}

function countConfiguredBackupCookies(value: string) {
  return parseBackupCookieRows(value).filter((row) => row.authToken || row.csrfToken).length;
}

function kindLabel(kind: JobKind) {
  const labels: Record<string, string> = {
    tweet_link: "推文链接",
    media_url: "媒体地址",
    user: "用户",
    list: "列表",
    following: "关注",
    failed_retry: "失败重试",
  };
  return labels[kind] ?? "未知类型";
}

function mediaTypeLabel(type: "photo" | "video" | "animated_gif" | "file") {
  return {
    photo: "图片",
    video: "视频",
    animated_gif: "GIF",
    file: "文件",
  }[type];
}

function getErrorMessage(error: unknown) {
  if (error instanceof Error) {
    return error.message;
  }
  if (typeof error === "string") {
    return error;
  }
  return "未知错误";
}
