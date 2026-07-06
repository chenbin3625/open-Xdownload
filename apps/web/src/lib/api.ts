export type JobStatus =
  | "pending"
  | "resolving"
  | "downloading"
  | "completed"
  | "failed"
  | "canceled";

export type JobKind =
  | "tweet_link"
  | "media_url"
  | "user"
  | "list"
  | "following";

export interface AppConfig {
  downloadDir: string;
  maxConcurrency: number;
  proxyUrl: string;
  authToken?: string;
  csrfToken?: string;
  autoRetryFailed: boolean;
  keepOriginalUrls: boolean;
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

export interface Dashboard {
  config: AppConfig;
  jobs: Job[];
  downloads: DownloadRecord[];
  failed: FailedMedia[];
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({ error: response.statusText }));
    throw new Error(payload.error ?? response.statusText);
  }
  return response.json() as Promise<T>;
}

export const getDashboard = () => api<Dashboard>("/api/dashboard");

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

export const createMediaDownload = (url: string) =>
  api<Job>("/api/download/media", {
    method: "POST",
    body: JSON.stringify({ url }),
  });

export const retryJob = (id: number) =>
  api<Job>(`/api/jobs/${id}/retry`, { method: "POST" });

export const cancelJob = (id: number) =>
  api<Job>(`/api/jobs/${id}/cancel`, { method: "POST" });

export const updateConfig = (config: AppConfig) =>
  api<AppConfig>("/api/config", {
    method: "PUT",
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
