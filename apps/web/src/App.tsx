import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CloudDownloadOutlined,
  CopyOutlined,
  DatabaseOutlined,
  DeleteOutlined,
  DownloadOutlined,
  ExclamationCircleOutlined,
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
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import {
  Alert,
  Avatar,
  Badge,
  Button,
  Card,
  Col,
  Descriptions,
  Drawer,
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
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Tree,
  Typography,
} from "antd";
import type { MenuProps, TableColumnsType, TabsProps, TreeDataNode } from "antd";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
  createLocalDirectory,
  deleteArchiveSchedule,
  deleteFailedTweet,
  formatBytes,
  getDashboard,
  getFailedTweets,
  listLocalDirectories,
  parseTweetLink,
  retryFailedTweets,
  retryJob,
  runArchiveSchedule,
  testStorage,
  updateArchiveSchedule,
  updateConfig,
} from "./lib/api";
import {
  cancelableStatuses,
  dashboardStatsWithJobStatusChange,
  isJobTerminal,
  jobStatusBucket,
  progressStatus,
  retryableStatuses,
} from "./lib/jobStatus";

const { Sider, Content } = Layout;
const { Text, Paragraph } = Typography;
const { TextArea } = Input;
const { DirectoryTree } = Tree;
const appIconPath = "/icon.svg";

type SectionKey = "overview" | "settings";
type TextTone = "secondary" | "success" | "warning" | "danger";
type RouteState = {
  section: SectionKey;
  jobPage: number;
  jobPageSize: number;
  shouldReplace: boolean;
};
type DashboardEvent = {
  type?: string;
  jobId?: number;
  payload?: unknown;
  timestamp?: string;
};
type BackupCookieRow = {
  authToken: string;
  csrfToken: string;
};
type CookieClientStatus = NonNullable<AuthCheck["diagnostics"]>["clients"][number];
type DirectoryTreeNode = TreeDataNode & {
  key: string;
  path: string;
  children?: DirectoryTreeNode[];
};

const fullWidthStyle: React.CSSProperties = { width: "100%" };
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

const settingsTips = {
  proxy: (
    <div className="form-tooltip-copy">
      <div>支持 http、https、socks5、socks5h，例如 http://127.0.0.1:7890。</div>
      <div>如需账号密码，可写成 socks5://user:password@127.0.0.1:1080。</div>
      <div>用户名或密码里的 @、:、/、% 需要 URL 编码；包含账号密码时会随配置保存在本地。</div>
    </div>
  ),
  concurrency: "后台同时运行的下载任务数，过高可能触发站点限流或增加远程存储压力。",
  maxFilenameLength: "限制保存到磁盘或远程存储的文件名长度，长推文文件名会自动截断。",
  fileNaming: "影响新下载文件的命名方式，已下载文件不会被重命名。",
  autoRetryFailed: "批量归档结束后，自动再次处理失败推文队列。",
  autoFollowProtected: "遇到未关注的保护账号时，使用已配置 Cookie 尝试发起关注后再归档。",
  includeNestedTweetMedia: "开启后会把引用或转推中的媒体也纳入单条下载和批量归档；关闭时只处理当前推文本体媒体。",
  authToken: "X / Twitter Cookie 中的 auth_token，用于登录态接口和批量归档。",
  csrfToken: "X / Twitter Cookie 中的 ct0，需与 auth_token 来自同一账号。",
  backupCookie: "多组备用 Cookie 会在批量归档时轮换使用，可降低单账号限流影响。",
  smbHost: "SMB 服务器地址，可填写 IP 或主机名，不要包含 smb:// 前缀。",
  smbPort: "SMB 默认端口通常为 445。",
  smbShare: "共享名是服务器上暴露的共享根名称，不是完整路径。",
  smbPath: "共享名下的保存目录，留空表示保存到共享根目录。",
  smbDomain: "多数家庭 NAS 可留空；企业域或工作组环境按实际要求填写。",
  remoteUsername: "远程存储账号用户名，留空时按匿名或服务端默认权限尝试。",
  savedSecret: "敏感字段读取时可能显示为 ********；保持不变或留空不会覆盖已有值。",
  webdavUrl: "WebDAV 服务根地址，例如 https://example.com/dav。",
  webdavPath: "WebDAV 根地址下的保存目录，留空表示保存到根目录。",
} satisfies Record<string, React.ReactNode>;

const defaultListPageSizeOptions = [5, 10, 20, 50];
const tablePageSizeOptions = [10, 20, 50, 100];
const failedTweetPageSizeOptions = [10, 20, 50];
const defaultJobPage = 1;
const defaultJobPageSize = 20;
const dashboardQueryRoot = ["dashboard"] as const;
const failedTweetQueryRoot = ["failed-tweets"] as const;
const sectionRoutes = {
  overview: "/overview",
  settings: "/settings",
} satisfies Record<SectionKey, string>;
const routeSections: Record<string, SectionKey> = {
  "/": "overview",
  "/overview": "overview",
  "/jobs": "overview",
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
  if (section === "overview") {
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

function buildDashboardQueryKey(jobPage: number, jobPageSize: number) {
  return ["dashboard", jobPage, jobPageSize] as const;
}

function refreshDashboardQueries(queryClient: QueryClient) {
  return queryClient.invalidateQueries({ queryKey: dashboardQueryRoot });
}

function parseDashboardEvent(raw: string): DashboardEvent | null {
  try {
    const parsed = JSON.parse(raw) as DashboardEvent;
    return parsed && typeof parsed === "object" ? parsed : null;
  } catch {
    return null;
  }
}

function isDashboardJobPayload(payload: unknown): payload is Job {
  if (!payload || typeof payload !== "object") return false;
  const job = payload as Partial<Job>;
  return typeof job.id === "number" && typeof job.status === "string";
}

function sameJob(left: Job, right: Job) {
  return (
    left.id === right.id &&
    left.kind === right.kind &&
    left.status === right.status &&
    left.input === right.input &&
    left.title === right.title &&
    left.progress === right.progress &&
    left.message === right.message &&
    left.error === right.error &&
    left.createdAt === right.createdAt &&
    left.updatedAt === right.updatedAt
  );
}

function patchDashboardJobCaches(queryClient: QueryClient, updatedJob: Job) {
  let found = false;
  let needsFullRefresh = false;

  queryClient.setQueriesData<Dashboard>({ queryKey: dashboardQueryRoot }, (current) => {
    if (!current) return current;

    const jobIndex = current.jobs.findIndex((job) => job.id === updatedJob.id);
    if (jobIndex === -1) return current;

    found = true;
    const previousJob = current.jobs[jobIndex];
    if (jobStatusBucket(previousJob.status) !== jobStatusBucket(updatedJob.status) || isJobTerminal(updatedJob.status)) {
      needsFullRefresh = true;
    }
    if (sameJob(previousJob, updatedJob)) return current;

    const jobs = [...current.jobs];
    jobs[jobIndex] = updatedJob;
    return {
      ...current,
      jobs,
      stats: dashboardStatsWithJobStatusChange(current.stats, previousJob.status, updatedJob.status),
    };
  });

  return { found, needsFullRefresh };
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
  const jobPage = section === "overview" ? parsePositiveInteger(params.get("page"), defaultJobPage) : defaultJobPage;
  const jobPageSize = section === "overview" ? parseJobPageSize(params.get("pageSize")) : defaultJobPageSize;
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
  const [manualRefreshPending, setManualRefreshPending] = useState(false);
  const refreshDashboard = useCallback(() => refreshDashboardQueries(queryClient), [queryClient]);

  const dashboard = useQuery({
    queryKey: buildDashboardQueryKey(jobPage, jobPageSize),
    queryFn: () => getDashboard({ page: jobPage, pageSize: jobPageSize }),
    placeholderData: (previousData) => previousData,
  });

  useEffect(() => {
    const events = new EventSource("/api/events");
    let timer: ReturnType<typeof setTimeout> | null = null;
    const scheduleRefresh = () => {
      if (timer) return;
      timer = setTimeout(() => {
        timer = null;
        refreshDashboard();
      }, 500);
    };
    events.onmessage = (message) => {
      const event = parseDashboardEvent(message.data);
      if (!event) {
        scheduleRefresh();
        return;
      }
      if (event.type === "job.updated" && isDashboardJobPayload(event.payload)) {
        const result = patchDashboardJobCaches(queryClient, event.payload);
        if (!result.found || result.needsFullRefresh) {
          scheduleRefresh();
        }
        return;
      }
      scheduleRefresh();
    };
    return () => {
      events.close();
      if (timer) clearTimeout(timer);
    };
  }, [queryClient, refreshDashboard]);

  useEffect(() => {
    const page = dashboard.data?.pagination.page;
    if (page && page !== jobPage) {
      setJobPage(page);
      if (activeSection === "overview") {
        updateBrowserRoute("overview", page, jobPageSize, true);
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
    if (activeSection === "overview") {
      updateBrowserRoute("overview", page, jobPageSize);
    }
  }

  function handleJobPageSizeChange(pageSize: number) {
    setJobPageSize(pageSize);
    setJobPage(1);
    if (activeSection === "overview") {
      updateBrowserRoute("overview", defaultJobPage, pageSize);
    }
  }

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
            <Tooltip title="刷新">
              <Button
                size="small"
                icon={<ReloadOutlined />}
                onClick={handleManualRefresh}
                loading={manualRefreshPending}
              />
            </Tooltip>
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

  return (
    <div className="workbench-page">
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
  onJobPageChange,
  onJobPageSizeChange,
}: {
  data: Dashboard;
  onJobPageChange: (page: number) => void;
  onJobPageSizeChange: (pageSize: number) => void;
}) {
  const screens = Grid.useBreakpoint();
  const [failedDrawerOpen, setFailedDrawerOpen] = useState(false);
  const failedTweetCount = data.failedTweetCount ?? 0;

  useEffect(() => {
    if (failedTweetCount === 0) {
      setFailedDrawerOpen(false);
    }
  }, [failedTweetCount]);

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
              onClick={() => setFailedDrawerOpen(true)}
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
        onClose={() => setFailedDrawerOpen(false)}
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
  const [parsedSourceUrl, setParsedSourceUrl] = useState("");
  const latestParseUrl = useRef("");
  const parsedHasMedia = parsed !== null && parsed.media.length > 0;

  const parseMutation = useMutation({
    mutationFn: (targetUrl: string) => parseTweetLink(targetUrl),
    onSuccess: (data, targetUrl) => {
      if (targetUrl !== latestParseUrl.current) {
        return;
      }
      setParsed(data);
      setParsedSourceUrl(targetUrl);
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
    mutationFn: () => createJob("tweet_link", parsedSourceUrl, parsed?.id ? `Tweet ${parsed.id}` : "推文任务"),
    onSuccess: () => {
      refreshDashboardQueries(queryClient);
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
    latestParseUrl.current = trimmed;
    setUrl(trimmed);
    setParsed(null);
    setParsedSourceUrl("");
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
          const nextUrl = event.target.value;
          latestParseUrl.current = nextUrl.trim();
          setUrl(nextUrl);
          setParsed(null);
          setParsedSourceUrl("");
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
              disabled={!parsedHasMedia || !parsedSourceUrl}
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
      refreshDashboardQueries(queryClient);
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
      refreshDashboardQueries(queryClient);
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
      refreshDashboardQueries(queryClient);
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
      refreshDashboardQueries(queryClient);
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
      refreshDashboardQueries(queryClient);
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
      className="mono-input"
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
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const failedTweetsQuery = useQuery({
    queryKey: [...failedTweetQueryRoot, page, pageSize],
    queryFn: () => getFailedTweets({ page, pageSize }),
  });
  const fallbackPagination: Dashboard["pagination"] = {
    page,
    pageSize,
    total,
    totalPages: total > 0 ? Math.ceil(total / pageSize) : 0,
  };
  const pageItems = failedTweetsQuery.data?.items ?? (page === 1 ? items.slice(0, pageSize) : []);
  const pagination = failedTweetsQuery.data?.pagination ?? fallbackPagination;
  const refreshFailedTweets = () => {
    queryClient.invalidateQueries({ queryKey: failedTweetQueryRoot });
    refreshDashboardQueries(queryClient);
  };
  const retryAll = useMutation({
    mutationFn: retryFailedTweets,
    onSuccess: (job) => {
      refreshFailedTweets();
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
      if (page > 1 && pageItems.length <= 1) {
        setPage(page - 1);
      }
      refreshFailedTweets();
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
      setPage(1);
      refreshFailedTweets();
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
        <Text type="secondary">{pagination.total > 0 ? `共 ${pagination.total} 条失败记录` : "暂无失败记录"}</Text>
        <Space size={8} wrap>
          <Button
            size="small"
            icon={<RetweetOutlined />}
            loading={retryAll.isPending}
            disabled={pagination.total === 0}
            onClick={() => retryAll.mutate()}
          >
            全部重试
          </Button>
          <Popconfirm
            title="清空失败队列"
            description="确认删除全部失败推文记录？"
            okText="清空"
            cancelText="取消"
            disabled={pagination.total === 0}
            onConfirm={() => clearAll.mutate()}
          >
            <Button
              size="small"
              danger
              icon={<DeleteOutlined />}
              loading={clearAll.isPending}
              disabled={pagination.total === 0}
            >
              清空
            </Button>
          </Popconfirm>
        </Space>
      </Toolbar>

      {failedTweetsQuery.isLoading && pageItems.length === 0 ? (
        <ListSkeleton rows={pageSize} />
      ) : pageItems.length === 0 ? (
        <AppEmpty description="暂无失败推文" />
      ) : (
        <LoadingSurface loading={failedTweetsQuery.isFetching}>
          <List
            bordered
            dataSource={pageItems}
            locale={{ emptyText: <AppEmpty description="暂无失败推文" /> }}
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
        </LoadingSurface>
      )}
      <AppPagination
        current={pagination.page}
        itemName="条记录"
        pageSize={pagination.pageSize}
        pageSizeOptions={failedTweetPageSizeOptions}
        total={pagination.total}
        onChange={(nextPage, nextPageSize) => {
          setPage(nextPageSize === pageSize ? nextPage : 1);
          setPageSize(nextPageSize);
        }}
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
  const activeJobIds = useMemo(
    () => jobs.filter((job) => cancelableStatuses.includes(job.status)).map((job) => job.id),
    [jobs],
  );

  useEffect(() => {
    if (activeJobIds.length === 0) return;

    setExpandedJobIds((current) => {
      const next = new Set(current);
      for (const id of activeJobIds) {
        next.add(id);
      }
      return next.size === current.length ? current : [...next];
    });
  }, [activeJobIds]);

  const retry = useMutation({
    mutationFn: retryJob,
    onSuccess: (job) => {
      refreshDashboardQueries(queryClient);
      notification.success({ message: `已创建重试任务 #${job.id}` });
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
      refreshDashboardQueries(queryClient);
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
            <Tooltip title={canRetry ? "新建重试任务" : "运行中任务不能重试"}>
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
    completed_with_errors: { color: "warning", icon: <CloseCircleOutlined />, label: "部分失败" },
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
  const [draftDirty, setDraftDirty] = useState(false);
  const [authResult, setAuthResult] = useState<AuthCheck | null>(null);
  const [authError, setAuthError] = useState("");
  const [authChecking, setAuthChecking] = useState(false);
  const pendingSavedConfigKey = useRef("");
  const autoCheckedCookieKey = useRef("");
  const authCheckSequence = useRef(0);
  const currentCookieKey = cookieCheckKey(draft);
  const currentCookieKeyRef = useRef(currentCookieKey);
  currentCookieKeyRef.current = currentCookieKey;

  useEffect(() => {
    const normalized = normalizeConfig(config);
    const configKey = configSyncKey(normalized);
    if (pendingSavedConfigKey.current) {
      if (pendingSavedConfigKey.current === configKey) {
        pendingSavedConfigKey.current = "";
        setDraft(normalized);
      }
      return;
    }
    if (!draftDirty) {
      setDraft(normalized);
    }
  }, [config, draftDirty]);

  function updateDraft(action: React.SetStateAction<AppConfig>) {
    pendingSavedConfigKey.current = "";
    setDraftDirty(true);
    setDraft(action);
  }

  function updateAuthDraft(action: React.SetStateAction<AppConfig>) {
    authCheckSequence.current += 1;
    setAuthResult(null);
    setAuthError("");
    setAuthChecking(false);
    updateDraft(action);
  }

  const mutation = useMutation({
    mutationFn: updateConfig,
    onSuccess: (updated) => {
      const normalized = normalizeConfig(updated);
      pendingSavedConfigKey.current = configSyncKey(normalized);
      setDraft(normalized);
      setDraftDirty(false);
      refreshDashboardQueries(queryClient);
      notification.success({ message: "配置已保存" });
    },
    onError: (error) => {
      notification.error({
        message: "保存失败",
        description: getErrorMessage(error),
      });
    },
  });

  async function runAuthCheck(submitted: AppConfig, notify: boolean) {
    const sequence = authCheckSequence.current + 1;
    const cookieKey = cookieCheckKey(submitted);
    authCheckSequence.current = sequence;
    setAuthChecking(true);
    setAuthError("");
    try {
      const result = await checkAuth(submitted);
      if (sequence !== authCheckSequence.current || cookieKey !== currentCookieKeyRef.current) {
        return;
      }
      setAuthResult(result);
      if (!notify) {
        return;
      }
      if (result.ok) {
        notification.success({
          message: "Cookie 检测通过",
          description: result.screenName ? `@${result.screenName}` : result.message,
        });
        return;
      }
      notification.warning({
        message: "Cookie 检测未通过",
        description: result.message,
      });
    } catch (error) {
      if (sequence !== authCheckSequence.current || cookieKey !== currentCookieKeyRef.current) {
        return;
      }
      const message = getErrorMessage(error);
      setAuthResult(null);
      setAuthError(message);
      if (notify) {
        notification.error({
          message: "Cookie 检测失败",
          description: message,
        });
      }
    } finally {
      if (sequence === authCheckSequence.current) {
        setAuthChecking(false);
      }
    }
  }

  useEffect(() => {
    const normalized = normalizeConfig(config);
    const cookieKey = cookieCheckKey(normalized);
    if (autoCheckedCookieKey.current === cookieKey) {
      return;
    }
    autoCheckedCookieKey.current = cookieKey;
    void runAuthCheck(normalized, false);
  }, [config]);

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
            loading={authChecking}
            onClick={() => void runAuthCheck(draft, true)}
          >
            检测 Cookie
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

      <div className="settings-main">
        <ConfigPanel kind="storage" icon={<DatabaseOutlined />} title="存储">
          <StorageSettings draft={draft} onChange={updateDraft} />
        </ConfigPanel>

        <ConfigPanel kind="download" icon={<DownloadOutlined />} title="下载">
          <DownloadSettingsFields draft={draft} onChange={updateDraft} onAuthChange={updateAuthDraft} />
        </ConfigPanel>

        <ConfigPanel
          kind="cookie"
          icon={<SafetyCertificateOutlined />}
          title="X Cookie"
        >
          <CookieSettingsFields
            authError={authError}
            authResult={authResult}
            checking={authChecking}
            draft={draft}
            onChange={updateAuthDraft}
          />
        </ConfigPanel>
      </div>
    </Form>
  );
}

function ConfigPanel({
  children,
  description,
  extra,
  icon,
  kind,
  title,
}: {
  children: React.ReactNode;
  description?: string;
  extra?: React.ReactNode;
  icon: React.ReactNode;
  kind: "storage" | "download" | "cookie";
  title: string;
}) {
  return (
    <section className={`settings-panel settings-panel-${kind}`}>
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

function DownloadSettingsFields({
  draft,
  onChange,
  onAuthChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  onAuthChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <Row gutter={[16, 0]} className="settings-field-grid">
      <Col xs={24} lg={12}>
        <Form.Item label="代理" tooltip={settingsTips.proxy}>
          <Input
            value={draft.proxyUrl}
            onChange={(event) => onAuthChange((current) => ({ ...current, proxyUrl: event.target.value }))}
            placeholder="http://127.0.0.1:7890"
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="并发" tooltip={settingsTips.concurrency}>
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
        <Form.Item label="最大文件名长度" tooltip={settingsTips.maxFilenameLength}>
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
        <Form.Item label="文件名命名" tooltip={settingsTips.fileNaming}>
          <Select
            value={draft.fileNamingMode}
            options={fileNamingOptions}
            onChange={(value) => onChange((current) => ({ ...current, fileNamingMode: value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="失败重试" tooltip={settingsTips.autoRetryFailed}>
          <Switch
            checked={draft.autoRetryFailed}
            onChange={(checked) => onChange((current) => ({ ...current, autoRetryFailed: checked }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="保护账号自动关注" tooltip={settingsTips.autoFollowProtected}>
          <Switch
            checked={draft.autoFollowProtected}
            onChange={(checked) => onChange((current) => ({ ...current, autoFollowProtected: checked }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Form.Item label="引用/转推媒体" tooltip={settingsTips.includeNestedTweetMedia}>
          <Switch
            checked={draft.includeNestedTweetMedia}
            onChange={(checked) => onChange((current) => ({ ...current, includeNestedTweetMedia: checked }))}
          />
        </Form.Item>
      </Col>
    </Row>
  );
}

function CookieSettingsFields({
  authError,
  authResult,
  checking,
  draft,
  onChange,
}: {
  authError: string;
  authResult: AuthCheck | null;
  checking: boolean;
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  const clients = authResult?.diagnostics?.clients ?? [];
  const primaryClient = clients.find((client) => client.primary) ?? clients[0];
  const backupClients = clients.filter((client) => !client.primary);
  const primaryComplete = Boolean((draft.authToken ?? "").trim() && (draft.csrfToken ?? "").trim());
  const sharedStatus = {
    checking,
    checked: Boolean(authResult),
    errorMessage: authError || (authResult && !authResult.diagnostics ? authResult.message : ""),
  };

  return (
    <div className="settings-cookie-fields">
      <div className="cookie-primary-fields">
        <Text strong className="cookie-group-title">主 Cookie</Text>
        <Row gutter={[16, 0]} className="settings-field-grid">
          <Col xs={24} lg={12}>
            <Form.Item label="auth_token" tooltip={settingsTips.authToken}>
              <Input
                aria-label="主 Cookie auth_token"
                prefix={<KeyOutlined />}
                suffix={(
                  <CookieTokenStatus
                    {...sharedStatus}
                    client={primaryClient}
                    hasValue={Boolean((draft.authToken ?? "").trim())}
                    pairComplete={primaryComplete}
                  />
                )}
                value={draft.authToken ?? ""}
                onChange={(event) => onChange((current) => ({ ...current, authToken: event.target.value }))}
                placeholder="输入 auth_token"
              />
            </Form.Item>
          </Col>
          <Col xs={24} lg={12}>
            <Form.Item label="ct0" tooltip={settingsTips.csrfToken}>
              <Input
                aria-label="主 Cookie ct0"
                prefix={<KeyOutlined />}
                suffix={(
                  <CookieTokenStatus
                    {...sharedStatus}
                    client={primaryClient}
                    hasValue={Boolean((draft.csrfToken ?? "").trim())}
                    pairComplete={primaryComplete}
                  />
                )}
                value={draft.csrfToken ?? ""}
                onChange={(event) => onChange((current) => ({ ...current, csrfToken: event.target.value }))}
                placeholder="输入 ct0"
              />
            </Form.Item>
          </Col>
        </Row>
      </div>
      <BackupCookieInputs
        clients={backupClients}
        {...sharedStatus}
        value={draft.additionalCookies ?? ""}
        onChange={(additionalCookies) => onChange((current) => ({ ...current, additionalCookies }))}
      />
    </div>
  );
}

function CookieTokenStatus({
  aggregateClients,
  checked,
  checking,
  client,
  errorMessage,
  hasValue,
  pairComplete,
}: {
  aggregateClients?: CookieClientStatus[];
  checked: boolean;
  checking: boolean;
  client?: CookieClientStatus;
  errorMessage: string;
  hasValue: boolean;
  pairComplete: boolean;
}) {
  let tone = "neutral";
  let label = "待检测";
  let detail = "保存或输入 Cookie 后可进行检测";
  let icon: React.ReactNode = <PauseCircleOutlined />;

  if (!hasValue) {
    label = "待配置";
    detail = "尚未填写此 token";
  } else if (!pairComplete) {
    tone = "warning";
    label = "待补全";
    detail = "auth_token 与 ct0 需要成对填写";
    icon = <ExclamationCircleOutlined />;
  } else if (checking) {
    tone = "checking";
    label = "检测中";
    detail = "正在检查 Cookie 状态";
    icon = <LoadingOutlined spin />;
  } else if (aggregateClients?.length) {
    const available = aggregateClients.filter((item) => item.ok).length;
    const hasTransientError = aggregateClients.some((item) => item.ok && item.error);
    tone = available === aggregateClients.length && !hasTransientError
      ? "success"
      : available === 0
        ? "error"
        : "warning";
    label = `${available}/${aggregateClients.length} 可用`;
    detail = aggregateClients
      .map((item) => cookieClientStatusDetail(item))
      .join("；");
    icon = tone === "success"
      ? <CheckCircleOutlined />
      : tone === "error"
        ? <CloseCircleOutlined />
        : <ExclamationCircleOutlined />;
  } else if (client) {
    if (!client.ok || client.disabled) {
      tone = "error";
      label = "异常";
      icon = <CloseCircleOutlined />;
    } else if (client.error) {
      tone = "warning";
      label = "暂时受限";
      icon = <ExclamationCircleOutlined />;
    } else {
      tone = "success";
      label = "有效";
      icon = <CheckCircleOutlined />;
    }
    detail = cookieClientStatusDetail(client);
  } else if (errorMessage) {
    tone = "error";
    label = "检测失败";
    detail = errorMessage;
    icon = <CloseCircleOutlined />;
  } else if (checked) {
    tone = "neutral";
    label = "未检测";
    detail = "此 Cookie 未进入检测队列，可能未填写完整或与其他 Cookie 重复";
  }

  return (
    <Tooltip title={detail}>
      <span className={`cookie-token-status cookie-token-status-${tone}`} aria-label={detail}>
        {icon}
        <span>{label}</span>
      </span>
    </Tooltip>
  );
}

function cookieClientStatusDetail(client: CookieClientStatus) {
  const account = client.screenName ? `@${client.screenName}` : `账号 ${client.index + 1}`;
  if (client.error) {
    return `${account}：${client.error}`;
  }
  return `${account}：Cookie 有效`;
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
  const createDirectory = useMutation({
    mutationFn: createLocalDirectory,
    onSuccess: (data) => {
      setRootPath(data.path);
      setSelectedPath(data.path);
      onSelect(data.path);
      const rootNode = listingToDirectoryTreeRoot(data);
      setTreeData([rootNode]);
      setExpandedKeys([rootNode.key]);
      notification.success({ message: "目录已创建并选择" });
    },
    onError: (error) => {
      notification.error({
        message: "创建目录失败",
        description: getErrorMessage(error),
      });
    },
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

  function openSelectedPath() {
    const trimmed = selectedPath.trim();
    if (!trimmed) return;
    setRootPath(trimmed);
  }

  function createSelectedPath() {
    const trimmed = selectedPath.trim();
    if (!trimmed) return;
    createDirectory.mutate(trimmed);
  }

  return (
    <Stack size={10}>
      <Space.Compact style={fullWidthStyle}>
        <Input
          prefix={<FolderOpenOutlined />}
          value={selectedPath}
          onChange={(event) => setSelectedPath(event.target.value)}
          onPressEnter={openSelectedPath}
        />
        <Button onClick={openSelectedPath} disabled={!selectedPath.trim() || listing.isLoading}>
          打开
        </Button>
        <Button onClick={createSelectedPath} loading={createDirectory.isPending} disabled={!selectedPath.trim()}>
          创建
        </Button>
        <Button type="primary" onClick={() => onSelect(selectedPath.trim())} disabled={!selectedPath.trim()}>
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
        <Form.Item label="主机" tooltip={settingsTips.smbHost}>
          <Input
            value={draft.smbHost}
            onChange={(event) => onChange((current) => ({ ...current, smbHost: event.target.value }))}
            placeholder="192.168.1.10"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="端口" tooltip={settingsTips.smbPort}>
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
        <Form.Item label="共享名" tooltip={settingsTips.smbShare}>
          <Input
            value={draft.smbShare}
            onChange={(event) => onChange((current) => ({ ...current, smbShare: event.target.value }))}
            placeholder="downloads"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="目录" tooltip={settingsTips.smbPath}>
          <Input
            value={draft.smbPath}
            onChange={(event) => onChange((current) => ({ ...current, smbPath: event.target.value }))}
            placeholder="x-media"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="域" tooltip={settingsTips.smbDomain}>
          <Input
            value={draft.smbDomain}
            onChange={(event) => onChange((current) => ({ ...current, smbDomain: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="用户名" tooltip={settingsTips.remoteUsername}>
          <Input
            value={draft.smbUsername}
            onChange={(event) => onChange((current) => ({ ...current, smbUsername: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24}>
        <Form.Item label="密码" tooltip={settingsTips.savedSecret}>
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
        <Form.Item label="地址" tooltip={settingsTips.webdavUrl}>
          <Input
            value={draft.webdavUrl}
            onChange={(event) => onChange((current) => ({ ...current, webdavUrl: event.target.value }))}
            placeholder="https://example.com/dav"
          />
        </Form.Item>
      </Col>
      <Col xs={24}>
        <Form.Item label="目录" tooltip={settingsTips.webdavPath}>
          <Input
            value={draft.webdavPath}
            onChange={(event) => onChange((current) => ({ ...current, webdavPath: event.target.value }))}
            placeholder="x-media"
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="用户名" tooltip={settingsTips.remoteUsername}>
          <Input
            value={draft.webdavUsername}
            onChange={(event) => onChange((current) => ({ ...current, webdavUsername: event.target.value }))}
          />
        </Form.Item>
      </Col>
      <Col xs={24} md={12}>
        <Form.Item label="密码" tooltip={settingsTips.savedSecret}>
          <Input.Password
            value={draft.webdavPassword ?? ""}
            onChange={(event) => onChange((current) => ({ ...current, webdavPassword: event.target.value }))}
          />
        </Form.Item>
      </Col>
    </Row>
  );
}

function BackupCookieInputs({
  checked,
  checking,
  clients,
  errorMessage,
  value,
  onChange,
}: {
  checked: boolean;
  checking: boolean;
  clients: CookieClientStatus[];
  errorMessage: string;
  value: string;
  onChange: (value: string) => void;
}) {
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
      tooltip={settingsTips.backupCookie}
      label={
        <span className="cookie-backup-heading">
          <Text strong>备用 Cookie</Text>
          <Tooltip title="添加备用 Cookie">
            <Button
              aria-label="添加备用 Cookie"
              className="cookie-add-button"
              size="small"
              type="text"
              icon={<PlusOutlined />}
              onClick={addRow}
            />
          </Tooltip>
        </span>
      }
    >
      <Stack size={8}>
        {rows.map((row, index) => {
          const pairComplete = Boolean(row.authToken.trim() && row.csrfToken.trim());
          const aggregateClients = isRedactedBackupCookieRow(row) && clients.length > 1 ? clients : undefined;
          const client = aggregateClients ? undefined : clients[index];
          const statusProps = { aggregateClients, checked, checking, client, errorMessage, pairComplete };
          return (
            <Row key={index} gutter={[8, 8]} align="middle" className="cookie-backup-row">
              <Col xs={24} md={11}>
                <Input
                  aria-label={`备用 Cookie ${index + 1} auth_token`}
                  prefix={<KeyOutlined />}
                  suffix={(
                    <CookieTokenStatus
                      {...statusProps}
                      hasValue={Boolean(row.authToken.trim())}
                    />
                  )}
                  value={row.authToken}
                  onChange={(event) => updateRow(index, "authToken", event.target.value)}
                  placeholder={`备用 ${index + 1} auth_token`}
                />
              </Col>
              <Col xs={24} md={11}>
                <Input
                  aria-label={`备用 Cookie ${index + 1} ct0`}
                  prefix={<KeyOutlined />}
                  suffix={(
                    <CookieTokenStatus
                      {...statusProps}
                      hasValue={Boolean(row.csrfToken.trim())}
                    />
                  )}
                  value={row.csrfToken}
                  onChange={(event) => updateRow(index, "csrfToken", event.target.value)}
                  placeholder={`备用 ${index + 1} ct0`}
                />
              </Col>
              <Col xs={24} md={2} className="cookie-row-action">
                <Tooltip title="删除备用 Cookie">
                  <Button
                    aria-label={`删除备用 Cookie ${index + 1}`}
                    className="cookie-remove-button"
                    danger
                    type="text"
                    icon={<DeleteOutlined />}
                    onClick={() => removeRow(index)}
                  />
                </Tooltip>
              </Col>
            </Row>
          );
        })}
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
    includeNestedTweetMedia: config.includeNestedTweetMedia ?? false,
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

function configSyncKey(config: AppConfig) {
  return JSON.stringify(config);
}

function cookieCheckKey(config: AppConfig) {
  return JSON.stringify([
    config.authToken ?? "",
    config.csrfToken ?? "",
    config.additionalCookies ?? "",
    config.proxyUrl ?? "",
  ]);
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

function storageTargetLabel(config: AppConfig) {
  if (config.storageType === "smb") {
    return config.smbShare ? `//${config.smbHost || "SMB"}/${config.smbShare}` : config.smbHost || "SMB 未配置";
  }
  if (config.storageType === "webdav") {
    return config.webdavUrl || "WebDAV 未配置";
  }
  return config.downloadDir || "本地目录未配置";
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
