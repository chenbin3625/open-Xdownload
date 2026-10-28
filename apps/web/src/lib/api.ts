import { invalidateSession, readSessionToken } from "./session";

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
  | "failed_retry";

export type FileNamingMode = "tweet_text" | "user_tweet";
export type StorageType = "local";

export interface AppConfig {
  downloadDir: string;
  maxConcurrency: number;
  proxyUrl: string;
  authToken?: string;
  csrfToken?: string;
  additionalCookies?: string;
  autoRetryFailed: boolean;
  autoFollowProtected: boolean;
  includeNestedTweetMedia: boolean;
  incrementalArchive: boolean;
  fileNamingMode: FileNamingMode;
  maxFilenameLength: number;
  storageType: StorageType;
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
}

export interface JobRequest {
  kind: JobKind;
  input: string;
  title?: string;
}

export interface BatchJobRequest {
  items: JobRequest[];
}

export interface ArchiveScheduleItem {
  kind: JobKind;
  input: string;
  title?: string;
}

export interface ArchiveSchedule {
  id: number;
  name: string;
  enabled: boolean;
  intervalMinutes: number;
  items: ArchiveScheduleItem[];
  lastRunAt?: string;
  nextRunAt: string;
  lastJobIds: number[];
  createdAt: string;
  updatedAt: string;
}

export interface ArchiveScheduleRequest {
  name: string;
  enabled: boolean;
  intervalMinutes: number;
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
  };
  media: Media[];
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
      const abort = () => controller.abort();
      external.addEventListener("abort", abort);
      timeoutSignal.addEventListener("abort", abort);
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
  const token = readSessionToken();
  try {
    const response = await fetch(path, {
      ...requestInit,
      credentials: "same-origin",
      signal: composed.signal,
      headers: {
        ...(method !== "GET" && method !== "HEAD" ? { "Content-Type": "application/json", "X-XDownload-Request": "1" } : {}),
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...(requestInit.headers ?? {}),
      },
    });
    if (!response.ok) {
      if (response.status === 401 && path !== "/api/auth/login" && token === readSessionToken()) {
        invalidateSession();
      }
      const payload: { error?: string } | null = await response.json().catch(() => null);
      // 空串 error、{}、HTTP/2 下为空的 statusText 都要继续回退，保证提示里总有内容。
      const message =
        (typeof payload?.error === "string" && payload.error) ||
        response.statusText ||
        `请求失败 (${response.status})`;
      throw new ApiError(message, response.status);
    }
    // 204/205 按协议就没有响应体，返回 undefined 由调用方决定是否使用。
    if (response.status === 204 || response.status === 205) {
      return undefined as T;
    }
    const body = await response.text();
    if (!body.trim()) {
      // 期望 JSON 的接口返回空体多半是反代/网关截断，必须显式报错，避免 undefined 流进缓存。
      throw new ApiError("空响应", response.status);
    }
    try {
      return JSON.parse(body) as T;
    } catch {
      throw new ApiError("响应格式无效", response.status);
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
  token: string;
}

export const getLoginSession = () => api<LoginSession>("/api/auth/session");
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
  signal,
}: { page?: number; pageSize?: number; signal?: AbortSignal } = {}) =>
  api<JobsPage>(`/api/jobs?page=${page}&pageSize=${pageSize}`, { signal });

export const getConfig = (signal?: AbortSignal) => api<AppConfig>("/api/config", { signal });

export const getJobFiles = (id: number, signal?: AbortSignal) =>
  api<JobFiles>(`/api/jobs/${id}/files`, { signal, timeoutMs: LONG_TIMEOUT_MS });
export const getLibraryDownloadsPage = (
  options: { page: number; pageSize: number; kind: string; user: string; search: string },
  signal?: AbortSignal,
) => {
  const params = new URLSearchParams({
    page: String(options.page),
    pageSize: String(options.pageSize),
    kind: options.kind,
    user: options.user,
    search: options.search,
  });
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

export const createJob = (kind: JobKind, input: string, title = "") =>
  api<Job>("/api/jobs", {
    method: "POST",
    body: JSON.stringify({ kind, input, title }),
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

export const cancelJob = (id: number) =>
  api<Job>(`/api/jobs/${id}/cancel`, { method: "POST" });

export const retryFailedTweets = () =>
  api<Job>("/api/failed-tweets/retry", { method: "POST" });

export const deleteFailedTweet = (id: number) =>
  api<{ ok: boolean }>(`/api/failed-tweets/${id}`, { method: "DELETE" });

export const clearFailedTweets = () =>
  api<{ ok: boolean }>("/api/failed-tweets", { method: "DELETE" });

export const updateConfig = (config: AppConfig) =>
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
