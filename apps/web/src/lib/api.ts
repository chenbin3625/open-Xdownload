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
export type StorageType = "local" | "smb" | "webdav";

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
  fileNamingMode: FileNamingMode;
  maxFilenameLength: number;
  storageType: StorageType;
  smbHost: string;
  smbPort: number;
  smbShare: string;
  smbPath: string;
  smbDomain: string;
  smbUsername: string;
  smbPassword?: string;
  webdavUrl: string;
  webdavPath: string;
  webdavUsername: string;
  webdavPassword?: string;
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
  filePath: string;
  bytes: number;
  createdAt: string;
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
  payload?: string;
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

export interface StorageTestResult {
  ok: boolean;
  type: StorageType;
  root: string;
  message: string;
  path: string;
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

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const method = (init?.method ?? "GET").toUpperCase();
  const response = await fetch(path, {
    ...init,
    headers: {
      ...(method !== "GET" && method !== "HEAD" ? { "Content-Type": "application/json" } : {}),
      ...(init?.headers ?? {}),
    },
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({ error: response.statusText }));
    throw new Error(payload.error ?? response.statusText);
  }
  return response.json() as Promise<T>;
}

export const getJobsPage = ({
  page = 1,
  pageSize = 20,
  signal,
}: { page?: number; pageSize?: number; signal?: AbortSignal } = {}) =>
  api<JobsPage>(`/api/jobs?page=${page}&pageSize=${pageSize}`, { signal });

export const getConfig = (signal?: AbortSignal) => api<AppConfig>("/api/config", { signal });

export const getJobFiles = (id: number, signal?: AbortSignal) => api<JobFiles>(`/api/jobs/${id}/files`, { signal });
export const getLibraryDownloads = (limit = 100, signal?: AbortSignal) =>
  api<DownloadRecord[]>(`/api/library/downloads?limit=${limit}`, { signal });

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
  api<Job[]>(`/api/archive-schedules/${id}/run`, { method: "POST" });

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
  });

export const testStorage = (config: AppConfig) =>
  api<StorageTestResult>("/api/storage/test", {
    method: "POST",
    body: JSON.stringify(config),
  });

export const listLocalDirectories = (path?: string, signal?: AbortSignal) =>
  api<LocalDirectoryListing>(`/api/local-directories${path ? `?path=${encodeURIComponent(path)}` : ""}`, { signal });

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
