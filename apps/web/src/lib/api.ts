import { t } from "./i18n";
import { invalidateSession, sessionRevision } from "./session";

export type JobStatus =
  | "pending"
  | "resolving"
  | "downloading"
  | "completed"
  | "completed_with_errors"
  | "failed"
  | "canceled";

export type JobKind =
  | "tweet_link"
  | "media_url"
  | "user"
  | "list"
  | "following"
  | "bookmarks"
  | "likes"
  | "thread"
  | "search"
  | "failed_retry"
  | "asset_migration";

export type ArchiveMode = "tweets" | "media" | "both";
export type AssetKind = "image" | "video" | "thumbnail" | "video_cover";

export type FileNamingMode = "tweet_text" | "user_tweet";
export type StorageType = "local";

export interface ArchiveFilter {
  mediaTypes?: string[];
  excludeRetweets?: boolean;
  excludeReplies?: boolean;
  minBytes?: number;
  minWidth?: number;
  minHeight?: number;
  sinceDate?: string;
  untilDate?: string;
}

export interface AppConfig {
  downloadDir: string;
  maxConcurrency: number;
  proxyUrl: string;
  autoRetryFailed: boolean;
  autoFollowProtected: boolean;
  includeNestedTweetMedia: boolean;
  incrementalArchive: boolean;
  fileNamingMode: FileNamingMode;
  maxFilenameLength: number;
  storageType: StorageType;
  archiveFilter?: ArchiveFilter | null;
  writeMetadataSidecar?: boolean;
  offlineThumbnails?: boolean;
}

export interface Job {
  id: number;
  kind: JobKind;
  status: JobStatus;
  input: string;
  title: string;
  progress: number;
  message: string;
  error?: string;
  createdAt: string;
  updatedAt: string;
  archiveMode?: ArchiveMode;
  backfill?: boolean;
  batchSize?: number;
  filter?: ArchiveFilter | null;
}

export interface JobRequest {
  kind: JobKind;
  input: string;
  title?: string;
  archiveMode?: ArchiveMode;
  backfill?: boolean;
  batchSize?: number;
  filter?: ArchiveFilter | null;
}

export interface BatchJobRequest {
  items: JobRequest[];
}

export interface ArchiveScheduleItem {
  kind: JobKind;
  input: string;
  title?: string;
  archiveMode?: ArchiveMode;
  backfill?: boolean;
  batchSize?: number;
  filter?: ArchiveFilter | null;
}

export interface ArchiveSchedule {
  id: number;
  name: string;
  enabled: boolean;
  intervalMinutes: number;
  cronExpr?: string;
  timezone?: string;
  items: ArchiveScheduleItem[];
  lastRunAt?: string;
  lastRunStatus?: "running" | "success" | "failed" | "canceled" | string;
  lastRunMessage?: string;
  nextRunAt: string;
  lastJobIds: number[];
  createdAt: string;
  updatedAt: string;
}

export interface ArchiveScheduleRequest {
  name: string;
  enabled: boolean;
  intervalMinutes: number;
  cronExpr?: string;
  timezone?: string;
  items: ArchiveScheduleItem[];
}

export interface MediaVariant {
  url: string;
  contentType: string;
  bitrate: number;
  quality: string;
}

export interface Media {
  id: string;
  type: "photo" | "video" | "animated_gif" | "file";
  url: string;
  previewUrl: string;
  bestUrl: string;
  variants: MediaVariant[];
  altText?: string;
  width?: number;
  height?: number;
}

export interface TweetArticle {
  id?: string;
  title?: string;
  previewText?: string;
  plainText?: string;
  coverImageUrl?: string;
  media?: Media[];
}

export interface PollChoice {
  label: string;
  votes: number;
}

export interface TweetPoll {
  id?: string;
  endDatetime?: string;
  votingStatus?: string;
  totalVotes?: number;
  choices: PollChoice[];
}

export interface TweetMetrics {
  likes?: number;
  retweets?: number;
  replies?: number;
  bookmarks?: number;
  views?: number;
}

export interface TweetData {
  id: string;
  url: string;
  text: string;
  createdAt: string;
  author: {
    id: string;
    name: string;
    screenName: string;
    avatarUrl?: string;
  };
  media: Media[];
  conversationId?: string;
  replyToId?: string;
  quotedTweetId?: string;
  retweetedTweetId?: string;
  related?: TweetData[];
  article?: TweetArticle;
  poll?: TweetPoll;
  metrics?: TweetMetrics;
}

export interface TagRecord {
  id: number;
  name: string;
  color: string;
  createdAt: string;
}

export interface CollectionRecord {
  id: number;
  name: string;
  description: string;
  tweetCount: number;
  downloadCount: number;
  createdAt: string;
  updatedAt: string;
}

export interface DeleteResult {
  deletedRecords: number;
  deletedFiles: number;
  skippedFiles: number;
}

export interface APIToken {
  id: number;
  name: string;
  tokenPrefix: string;
  scopes: string;
  lastUsedAt?: string;
  expiresAt?: string;
  createdAt: string;
}

export interface APITokenCreateResponse {
  token: APIToken;
  plaintext: string;
}

export type WebhookKind = "generic" | "telegram" | "discord" | "bark" | "feishu" | "wecom" | "dingtalk";

export interface Webhook {
  id: number;
  name: string;
  kind: WebhookKind | string;
  url: string;
  secret?: string;
  events: string[];
  enabled: boolean;
  lastStatus?: string;
  lastError?: string;
  lastSentAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface WebhookPayload {
  name: string;
  kind: string;
  url: string;
  secret?: string;
  events: string[];
  enabled: boolean;
}

export interface KindStat {
  count: number;
  bytes: number;
}

export interface StorageStats {
  downloadDir: string;
  diskTotalBytes: number;
  diskFreeBytes: number;
  diskAvailable: boolean;
  totalTweets: number;
  totalDownloads: number;
  totalBytes: number;
  byKind: Record<string, KindStat>;
  topUsers: LibraryUserSummary[];
  duplicateGroups: number;
  duplicateBytes: number;
  databaseBytes: number;
}

export interface BrokenDownloadItem {
  id: number;
  tweetId: string;
  filePath: string;
  bytes: number;
}

export interface OrphanFileItem {
  filePath: string;
  bytes: number;
  modTime: string;
}

export interface DuplicateHashGroup {
  contentHash: string;
  filePaths: string[];
  bytes: number;
}

export interface MaintenanceScanResult {
  brokenRecords: BrokenDownloadItem[];
  orphanFiles: OrphanFileItem[];
  duplicateGroups: DuplicateHashGroup[];
  brokenCount: number;
  orphanCount: number;
  duplicateCount: number;
  reclaimableBytes: number;
}

export interface MaintenanceCleanupResult {
  action: string;
  affected: number;
  reclaimedBytes: number;
  skipped: number;
  createdJobId?: number;
}

export interface ConfigImportResult {
  ok: boolean;
  createdSchedules: number;
  createdTags: number;
  createdCollections: number;
}

export interface DownloadRecord {
  id: number;
  jobId: number;
  tweetId: string;
  mediaUrl: string;
  previewUrl?: string;
  filePath: string;
  bytes: number;
  createdAt: string;
  userScreenName?: string;
  userName?: string;
  fileUrl?: string;
  assetUrls?: Partial<Record<AssetKind, string>>;
  starred?: boolean;
  tags?: TagRecord[];
}

export interface FailedMedia {
  id: number;
  jobId: number;
  mediaUrl: string;
  error: string;
  createdAt: string;
}

export interface RateLimitSnapshot {
  path: string;
  limit: number;
  remaining: number;
  reset: string;
  blocked: boolean;
}

export interface ClientStatus {
  index: number;
  primary: boolean;
  screenName: string;
  ok: boolean;
  disabled: boolean;
  error?: string;
  requestCount: number;
  rateLimits: RateLimitSnapshot[];
}

export interface PoolDiagnostics {
  total: number;
  available: number;
  clients: ClientStatus[];
}

export interface FailedTweet {
  id: number;
  jobId: number;
  entityId: number;
  tweetId: string;
  error: string;
  createdAt: string;
  updatedAt: string;
  jobTitle: string;
  entityName: string;
  entityParentDir: string;
  userId: string;
  userScreenName: string;
  userName: string;
}

export interface AuthCheck {
  configured: boolean;
  ok: boolean;
  screenName?: string;
  message: string;
  diagnostics?: PoolDiagnostics;
}

/** 当前生效的 X 爬虫实现，由后端 OPEN_XDOWNLOAD_SCRAPER 决定。 */
export type XScraperImplementation = "legacy" | "xscrape";

/** 账号来源：db = 数据库导入，env = 环境变量提供（只读）。 */
export type XAccountSource = "db" | "env";

export interface XAccountLock {
  /** GraphQL operation 名；"*" 表示账号级冷却。 */
  queue: string;
  until: string;
  reason: string;
}

export interface XAccountQueueRequests {
  queue: string;
  requests: number;
}

/** GET /api/x-accounts 返回的账号视图；authToken / proxy 已由后端打码。 */
export interface XAccountView {
  id: number;
  screenName: string;
  authToken: string;
  csrfToken: string;
  hasExtraCookies: boolean;
  userAgent: string;
  proxy: string;
  isPrimary: boolean;
  source: XAccountSource;
  active: boolean;
  errorMsg: string;
  lastUsed: string | null;
  verifiedAt: string | null;
  createdAt: string;
  locks: XAccountLock[];
  requestsToday: number;
  requestsByQueue: XAccountQueueRequests[];
}

export interface XAccountsResponse {
  scraper: XScraperImplementation;
  accounts: XAccountView[];
}

export interface XAccountCreateRequest {
  cookie: string;
  userAgent?: string;
  proxy?: string;
  isPrimary?: boolean;
}

export interface XAccountImportIssue {
  /** 导入文本中的条目序号（0 起）。 */
  index: number;
  reason: string;
}

export interface XAccountImportResult {
  created: XAccountView[];
  issues: XAccountImportIssue[];
}

export interface XAccountPatch {
  active?: boolean;
  isPrimary?: boolean;
  userAgent?: string;
  proxy?: string;
  /** 替换 Cookie；env 账号会被后端拒绝。 */
  cookie?: string;
}

export interface XAccountCheckResult {
  ok: boolean;
  screenName?: string;
  error?: string;
  account: XAccountView;
}

export interface XAccountCheckAllItem {
  id: number;
  ok: boolean;
  screenName?: string;
  error?: string;
}

export interface XAccountCheckAllResult {
  results: XAccountCheckAllItem[];
  accounts: XAccountView[];
}

export interface LocalDirectoryEntry {
  name: string;
  path: string;
  hasChildren: boolean;
}

export interface LocalDirectoryListing {
  path: string;
  parent: string;
  entries: LocalDirectoryEntry[];
}

export interface DashboardPagination {
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

export interface DashboardStats {
  total: number;
  active: number;
  completed: number;
  failed: number;
}

export interface Dashboard {
  config?: AppConfig;
  jobs: Job[];
  downloads?: DownloadRecord[];
  failed?: FailedMedia[];
  failedTweets?: FailedTweet[];
  failedTweetCount: number;
  archiveSchedules?: ArchiveSchedule[];
  pagination: DashboardPagination;
  stats: DashboardStats;
}

export interface DashboardMeta {
  stats: DashboardStats;
  failedTweetCount: number;
}

export interface JobsPage {
  items: Job[];
  page: number;
  pageSize: number;
  total?: number;
  counts?: { all: number; active: number; completed: number; failed: number };
}

export interface JobFiles {
  downloads: DownloadRecord[];
  failed: FailedMedia[];
  truncated?: boolean;
}

export interface LibraryDownloadsPage {
  items: DownloadRecord[];
  pagination: DashboardPagination;
  counts: { all: number; images: number; videos: number; gifs: number };
  users: { value: string; label: string }[];
  truncated: boolean;
}

export const jobFilesQueryRoot = ["job-files"] as const;
export const jobsQueryRoot = ["jobs"] as const;
export const dashboardMetaQueryRoot = ["dashboard-meta"] as const;
export const configQueryRoot = ["config"] as const;
export const archiveScheduleQueryRoot = ["archive-schedules"] as const;
export const failedTweetQueryRoot = ["failed-tweets"] as const;
export const libraryDownloadsQueryRoot = ["library-downloads"] as const;
export const xAccountsQueryRoot = ["x-accounts"] as const;
export const tagsQueryRoot = ["tags"] as const;
export const collectionsQueryRoot = ["collections"] as const;
export const webhooksQueryRoot = ["webhooks"] as const;
export const apiTokensQueryRoot = ["api-tokens"] as const;
export const maintenanceStatsQueryRoot = ["maintenance-stats"] as const;

/** 归档库按账号聚合的统计（用户卡片视图）。 */
export interface LibraryUserSummary {
  screenName: string;
  displayName: string;
  avatarUrl: string;
  mediaCount: number;
  totalBytes: number;
  latestAt: string;
}

export interface LibraryUsersPage {
  items: LibraryUserSummary[];
  pagination?: DashboardPagination;
}

export interface LibraryUsersQueryOptions {
  page?: number;
  pageSize?: number;
  search?: string;
  signal?: AbortSignal;
}

export const libraryUsersQueryRoot = ["library-users"] as const;

export const getLibraryUsers = (arg?: AbortSignal | LibraryUsersQueryOptions) => {
  const isSignal = Boolean(arg && typeof arg === "object" && "aborted" in arg);
  const options: LibraryUsersQueryOptions = isSignal ? { signal: arg as AbortSignal } : ((arg as LibraryUsersQueryOptions | undefined) ?? {});
  const params = new URLSearchParams();
  if (options.page !== undefined) params.set("page", String(options.page));
  if (options.pageSize !== undefined) params.set("pageSize", String(options.pageSize));
  if (options.search !== undefined && options.search.trim() !== "") params.set("search", options.search.trim());
  const qs = params.toString();
  return api<LibraryUsersPage>(`/api/library/users${qs ? `?${qs}` : ""}`, {
    signal: options.signal,
    timeoutMs: LONG_TIMEOUT_MS,
  });
};

export interface FailedTweetPage {
  items: FailedTweet[];
  pagination: DashboardPagination;
}

// 仍是 Error 子类，既有的 instanceof Error / error.message 调用方不受影响；额外带上 HTTP 状态码。
export class ApiError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

// 默认 30 秒超时；媒体库分页、配置写入、Cookie 检测等"已知可能很慢"的请求用 60 秒。
export const DEFAULT_TIMEOUT_MS = 30_000;
export const LONG_TIMEOUT_MS = 60_000;

export interface ApiInit extends RequestInit {
  /** 单次请求超时（毫秒）；传 null 表示不超时，默认 DEFAULT_TIMEOUT_MS。 */
  timeoutMs?: number | null;
}

/**
 * 组合"调用方取消"与"超时中止"两个 signal。
 * 老浏览器（Safari < 17.4 / Firefox < 124）没有 AbortSignal.any/timeout：
 * - 只有一方存在时直接用那一方；
 * - 两方都存在时手写组合；AbortController 也不存在时退化为不超时（宁可慢也不炸）。
 */
function createRequestSignal(
  external: AbortSignal | null | undefined,
  timeoutMs: number | null,
): { signal: AbortSignal | undefined; dispose: () => void } {
  const hasTimeout = typeof timeoutMs === "number" && Number.isFinite(timeoutMs) && timeoutMs > 0;
  const canCreateController = typeof AbortController === "function";
  const canAny = typeof AbortSignal !== "undefined" && typeof AbortSignal.any === "function";

  let timeoutSignal: AbortSignal | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let removeAbortListeners: (() => void) | undefined;

  if (hasTimeout) {
    if (typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout === "function") {
      timeoutSignal = AbortSignal.timeout(timeoutMs);
    } else if (canCreateController) {
      const controller = new AbortController();
      timer = setTimeout(() => controller.abort(), timeoutMs);
      timeoutSignal = controller.signal;
    }
  }

  let signal: AbortSignal | undefined;
  if (timeoutSignal && external) {
    if (canAny) {
      signal = AbortSignal.any([external, timeoutSignal]);
    } else if (canCreateController) {
      // 手写组合：任一方中止都触发同一个 controller。
      const controller = new AbortController();
      const onExternalAbort = () => controller.abort(external.reason);
      const onTimeoutAbort = () => controller.abort(timeoutSignal?.reason);
      external.addEventListener("abort", onExternalAbort);
      timeoutSignal.addEventListener("abort", onTimeoutAbort);
      removeAbortListeners = () => {
        external.removeEventListener("abort", onExternalAbort);
        timeoutSignal?.removeEventListener("abort", onTimeoutAbort);
      };
      if (external.aborted) onExternalAbort();
      else if (timeoutSignal.aborted) onTimeoutAbort();
      signal = controller.signal;
    } else {
      signal = external; // 无法组合时不牺牲"调用方取消"
    }
  } else {
    signal = timeoutSignal ?? external ?? undefined;
  }

  return {
    signal,
    dispose: () => {
      removeAbortListeners?.();
      if (timer) {
        clearTimeout(timer);
        timer = null;
      }
    },
  };
}

export async function api<T>(path: string, init?: ApiInit): Promise<T> {
  const { timeoutMs = DEFAULT_TIMEOUT_MS, ...requestInit } = init ?? {};
  const method = (requestInit.method ?? "GET").toUpperCase();
  const composed = createRequestSignal(requestInit.signal, timeoutMs);
  const revision = sessionRevision();
  try {
    const response = await fetch(path, {
      ...requestInit,
      credentials: "same-origin",
      signal: composed.signal,
      headers: {
        ...(method !== "GET" && method !== "HEAD" ? { "Content-Type": "application/json", "X-XDownload-Request": "1" } : {}),
        ...(requestInit.headers ?? {}),
      },
    });
    if (!response.ok) {
      if (response.status === 401 && path !== "/api/auth/login" && revision === sessionRevision()) {
        invalidateSession();
      }
      const payload: { error?: string } | null = await response.json().catch(() => null);
      // 空串 error、{}、HTTP/2 下为空的 statusText 都要继续回退，保证提示里总有内容。
      const message =
        (typeof payload?.error === "string" && payload.error) ||
        response.statusText ||
        t("请求失败 ({{value0}})", { value0: response.status });
      throw new ApiError(message, response.status);
    }
    // 204/205 按协议就没有响应体，返回 undefined 由调用方决定是否使用。
    if (response.status === 204 || response.status === 205) {
      return undefined as T;
    }
    const body = await response.text();
    if (!body.trim()) {
      // 期望 JSON 的接口返回空体多半是反代/网关截断，必须显式报错，避免 undefined 流进缓存。
      throw new ApiError(t("空响应"), response.status);
    }
    try {
      return JSON.parse(body) as T;
    } catch {
      throw new ApiError(t("响应格式无效"), response.status);
    }
  } finally {
    composed.dispose();
  }
}

export interface LoginSession {
  configured: boolean;
  authenticated: boolean;
  username?: string;
}

export interface LoginResult {
  username: string;
  token?: string;
}

export const getLoginSession = () => api<LoginSession>("/api/auth/session");
export const logoutAccount = () => api<{ ok: boolean }>("/api/auth/logout", { method: "POST" });
export const loginAccount = (username: string, password: string, setup: boolean) =>
  api<LoginResult>(setup ? "/api/auth/setup" : "/api/auth/login", {
    method: "POST", body: JSON.stringify({ username, password }),
  });
export const changeAccountPassword = (currentPassword: string, newPassword: string) =>
  api<{ ok: boolean }>("/api/auth/password", {
    method: "PUT", body: JSON.stringify({ currentPassword, newPassword }),
  });

export const getJobsPage = ({
  page = 1,
  pageSize = 10,
  status = "all",
  kind = "all",
  search = "",
  signal,
}: { page?: number; pageSize?: number; status?: string; kind?: string; search?: string; signal?: AbortSignal } = {}) => {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  if (status && status !== "all") params.set("status", status);
  if (kind && kind !== "all") params.set("kind", kind);
  if (search.trim()) params.set("q", search.trim());
  return api<JobsPage>(`/api/jobs?${params}`, { signal });
};

export const getConfig = (signal?: AbortSignal) => api<AppConfig>("/api/config", { signal });

export const getJobFiles = (id: number, signal?: AbortSignal) =>
  api<JobFiles>(`/api/jobs/${id}/files`, { signal, timeoutMs: LONG_TIMEOUT_MS });
export const getLibraryDownloadsPage = (
  options: {
    page: number;
    pageSize: number;
    kind: string;
    user: string;
    search: string;
    sort?: string;
    from?: string;
    to?: string;
    minBytes?: string;
    maxBytes?: string;
    starred?: string;
    tagId?: string;
    collectionId?: string;
    omitUsers?: boolean;
  },
  signal?: AbortSignal,
) => {
  const params = new URLSearchParams({
    page: String(options.page),
    pageSize: String(options.pageSize),
    kind: options.kind,
    user: options.user,
    search: options.search,
  });
  for (const key of ["sort", "from", "to", "minBytes", "maxBytes", "starred", "tagId", "collectionId"] as const) {
    if (options[key] !== undefined && options[key] !== "") params.set(key, options[key]);
  }
  if (options.omitUsers) {
    params.set("omitUsers", "1");
  }
  return api<LibraryDownloadsPage>(`/api/library/downloads?${params}`, {
    signal,
    timeoutMs: LONG_TIMEOUT_MS,
  });
};


export const getDashboardMeta = (signal?: AbortSignal) => api<DashboardMeta>("/api/dashboard/meta", { signal });

export const getArchiveSchedules = (signal?: AbortSignal) => api<ArchiveSchedule[]>("/api/archive-schedules", { signal });

export const parseTweetLink = (url: string) =>
  api<TweetData>("/api/parse/tweet-link", {
    method: "POST",
    body: JSON.stringify({ url }),
  });

export const createJob = (
  kind: JobKind,
  input: string,
  title = "",
  options?: { archiveMode: ArchiveMode; backfill: boolean; filter?: ArchiveFilter | null },
) =>
  api<Job>("/api/jobs", {
    method: "POST",
    body: JSON.stringify({ kind, input, title, ...options }),
  });

export const createJobsBatch = (request: BatchJobRequest) =>
  api<Job[]>("/api/jobs/batch", {
    method: "POST",
    body: JSON.stringify(request),
    timeoutMs: LONG_TIMEOUT_MS,
  });

export const createArchiveSchedule = (request: ArchiveScheduleRequest) =>
  api<ArchiveSchedule>("/api/archive-schedules", {
    method: "POST",
    body: JSON.stringify(request),
  });

export const updateArchiveSchedule = (id: number, request: ArchiveScheduleRequest) =>
  api<ArchiveSchedule>(`/api/archive-schedules/${id}`, {
    method: "PUT",
    body: JSON.stringify(request),
  });

export const deleteArchiveSchedule = (id: number) =>
  api<{ ok: boolean }>(`/api/archive-schedules/${id}`, { method: "DELETE" });

export const runArchiveSchedule = (id: number) =>
  api<Job[]>(`/api/archive-schedules/${id}/run`, { method: "POST", timeoutMs: LONG_TIMEOUT_MS });

export const retryJob = (id: number) =>
  api<Job>(`/api/jobs/${id}/retry`, { method: "POST" });

// 删除已结束的任务及其关联记录；活动中的任务由后端返回 409。
export const deleteJob = (id: number) =>
  api<{ ok: boolean }>(`/api/jobs/${id}`, { method: "DELETE" });

export const cancelJob = (id: number) =>
  api<Job>(`/api/jobs/${id}/cancel`, { method: "POST" });

export const retryFailedTweets = () =>
  api<Job>("/api/failed-tweets/retry", { method: "POST" });

export const deleteFailedTweet = (id: number) =>
  api<{ ok: boolean }>(`/api/failed-tweets/${id}`, { method: "DELETE" });

export const clearFailedTweets = () =>
  api<{ ok: boolean }>("/api/failed-tweets", { method: "DELETE" });

export const updateConfig = (config: Partial<AppConfig>) =>
  api<AppConfig>("/api/config", {
    method: "PUT",
    body: JSON.stringify(config),
    // 改下载目录会触发历史路径重写，可能超过默认 30s。
    timeoutMs: LONG_TIMEOUT_MS,
  });

export const listLocalDirectories = (path?: string, signal?: AbortSignal) =>
  api<LocalDirectoryListing>(`/api/local-directories${path ? `?path=${encodeURIComponent(path)}` : ""}`, {
    signal,
    // 网络盘/大目录枚举可能很慢。
    timeoutMs: LONG_TIMEOUT_MS,
  });

export const createLocalDirectory = (path: string) =>
  api<LocalDirectoryListing>("/api/local-directories", {
    method: "POST",
    body: JSON.stringify({ path }),
  });

export const getFailedTweets = ({
  page = 1,
  pageSize = 20,
  signal,
}: { page?: number; pageSize?: number; signal?: AbortSignal } = {}) =>
  api<FailedTweetPage>(`/api/failed-tweets?page=${page}&pageSize=${pageSize}`, { signal });

export const checkAuth = (config: AppConfig) =>
  api<AuthCheck>("/api/auth/check", {
    method: "POST",
    body: JSON.stringify(config),
    // 后端自身有 20s 超时，这里必须留足余量。
    timeoutMs: LONG_TIMEOUT_MS,
  });

// X 账号池。导入/检测可能逐个访问 X，使用与配置写入相同的长超时。
export const getXAccounts = (signal?: AbortSignal) =>
  api<XAccountsResponse>("/api/x-accounts", { signal });

export const createXAccount = (request: XAccountCreateRequest) =>
  api<XAccountView>("/api/x-accounts", {
    method: "POST",
    body: JSON.stringify(request),
  });

export const importXAccounts = (text: string) =>
  api<XAccountImportResult>("/api/x-accounts/import", {
    method: "POST",
    body: JSON.stringify({ text }),
    timeoutMs: LONG_TIMEOUT_MS,
  });

export const updateXAccount = (id: number, patch: XAccountPatch) =>
  api<XAccountView>(`/api/x-accounts/${id}`, {
    method: "PATCH",
    body: JSON.stringify(patch),
  });

export const deleteXAccount = (id: number) =>
  api<void>(`/api/x-accounts/${id}`, { method: "DELETE" });

export const checkXAccount = (id: number) =>
  api<XAccountCheckResult>(`/api/x-accounts/${id}/check`, {
    method: "POST",
    timeoutMs: LONG_TIMEOUT_MS,
  });

export const unlockXAccount = (id: number) =>
  api<XAccountView>(`/api/x-accounts/${id}/unlock`, { method: "POST" });

export const checkAllXAccounts = () =>
  api<XAccountCheckAllResult>("/api/x-accounts/check-all", {
    method: "POST",
    timeoutMs: LONG_TIMEOUT_MS,
  });

// Personal API Tokens
export const getApiTokens = (signal?: AbortSignal) => api<APIToken[]>("/api/tokens", { signal });
export const createApiToken = (req: { name: string; scopes?: string; expiresInDays?: number }) =>
  api<APITokenCreateResponse>("/api/tokens", { method: "POST", body: JSON.stringify(req) });
export const deleteApiToken = (id: number) => api<void>(`/api/tokens/${id}`, { method: "DELETE" });

// Webhooks & push notifications
export const getWebhooks = (signal?: AbortSignal) => api<Webhook[]>("/api/webhooks", { signal });
export const createWebhook = (payload: WebhookPayload) =>
  api<Webhook>("/api/webhooks", { method: "POST", body: JSON.stringify(payload) });
export const updateWebhook = (id: number, payload: WebhookPayload) =>
  api<Webhook>(`/api/webhooks/${id}`, { method: "PUT", body: JSON.stringify(payload) });
export const deleteWebhook = (id: number) => api<void>(`/api/webhooks/${id}`, { method: "DELETE" });
export const testWebhook = (id: number) =>
  api<{ ok: boolean; webhook: Webhook }>(`/api/webhooks/${id}/test`, { method: "POST", timeoutMs: LONG_TIMEOUT_MS });

// Tags, Collections & Download management
export const getTags = (signal?: AbortSignal) => api<TagRecord[]>("/api/tags", { signal });
export const createTag = (name: string, color = "blue") =>
  api<TagRecord>("/api/tags", { method: "POST", body: JSON.stringify({ name, color }) });
export const updateTag = (id: number, name: string, color: string) =>
  api<TagRecord>(`/api/tags/${id}`, { method: "PUT", body: JSON.stringify({ name, color }) });
export const deleteTag = (id: number) => api<void>(`/api/tags/${id}`, { method: "DELETE" });

export const getCollections = (signal?: AbortSignal) => api<CollectionRecord[]>("/api/collections", { signal });
export const createCollection = (name: string, description = "") =>
  api<CollectionRecord>("/api/collections", { method: "POST", body: JSON.stringify({ name, description }) });
export const updateCollection = (id: number, name: string, description = "") =>
  api<CollectionRecord>(`/api/collections/${id}`, { method: "PUT", body: JSON.stringify({ name, description }) });
export const deleteCollection = (id: number) => api<void>(`/api/collections/${id}`, { method: "DELETE" });
export const addCollectionItems = (id: number, itemType: "tweet" | "download", itemIds: string[]) =>
  api<{ ok: boolean }>(`/api/collections/${id}/items`, { method: "POST", body: JSON.stringify({ itemType, itemIds }) });
export const removeCollectionItems = (id: number, itemType: "tweet" | "download", itemIds: string[]) =>
  api<{ ok: boolean }>(`/api/collections/${id}/items`, { method: "DELETE", body: JSON.stringify({ itemType, itemIds }) });

export const setDownloadStar = (id: number, starred: boolean) =>
  api<{ starred: boolean }>(`/api/library/downloads/${id}/star`, { method: "POST", body: JSON.stringify({ starred }) });
export const setDownloadTags = (id: number, tagIds: number[]) =>
  api<{ tags: TagRecord[] }>(`/api/library/downloads/${id}/tags`, { method: "PUT", body: JSON.stringify({ tagIds }) });
export const deleteDownload = (id: number, deleteFiles = false) =>
  api<DeleteResult>(`/api/library/downloads/${id}${deleteFiles ? "?deleteFiles=1" : ""}`, { method: "DELETE" });
export const batchDeleteDownloads = (ids: number[], deleteFiles = false) =>
  api<DeleteResult>("/api/library/downloads/batch-delete", { method: "POST", body: JSON.stringify({ ids, deleteFiles }) });

export async function exportMediaZip(payload: {
  ids?: number[];
  user?: string;
  kind?: string;
  search?: string;
  starred?: boolean;
  tagId?: number;
  collectionId?: number;
}): Promise<Blob> {
  const response = await fetch("/api/export/media-zip", {
    method: "POST",
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-XDownload-Request": "1",
    },
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    const msg = (typeof body?.error === "string" && body.error) || response.statusText || t("导出失败");
    throw new ApiError(msg, response.status);
  }
  return response.blob();
}

// Maintenance & Storage analytics
export const getMaintenanceStats = (signal?: AbortSignal) =>
  api<StorageStats>("/api/maintenance/stats", { signal, timeoutMs: LONG_TIMEOUT_MS });
export const scanMaintenance = () =>
  api<MaintenanceScanResult>("/api/maintenance/scan", { method: "POST", timeoutMs: LONG_TIMEOUT_MS });
export const cleanupMaintenance = (action: "prune_broken" | "delete_orphans" | "import_orphans" | "dedup_hardlink") =>
  api<MaintenanceCleanupResult>("/api/maintenance/cleanup", {
    method: "POST",
    body: JSON.stringify({ action }),
    timeoutMs: LONG_TIMEOUT_MS,
  });
export const importConfigBundle = (bundle: unknown) =>
  api<ConfigImportResult>("/api/maintenance/import-config", {
    method: "POST",
    body: JSON.stringify(bundle),
    timeoutMs: LONG_TIMEOUT_MS,
  });

export function formatBytes(bytes: number) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  return `${value.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}
