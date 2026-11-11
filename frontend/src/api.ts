export interface CookieStatus {
  status: string;
  reason: string;
  checked_at: string | null;
}
export interface Settings {
  sync_interval_minutes: number;
  download_concurrency: number;
  image_path: string;
  video_path: string;
  auto_download: boolean;
  relationship_sync_enabled: boolean;
  relationship_interval_minutes: number;
  proxy_url: string;
}
export interface Storage {
  media_bytes: number;
  database_bytes: number;
  free_bytes: number;
  total_bytes: number;
}
export interface User {
  id: string;
  username: string;
  name: string;
  bio: string;
  enabled: boolean;
  sync_interval: number | null;
  last_sync: string | null;
  last_error: string | null;
  tweet_count: number;
  media_count: number;
  created_at: string;
}
export interface Tweet {
  id: string;
  user_id: string | null;
  author: string;
  author_name: string;
  text: string;
  published_at: string;
  url: string;
  media_count: number;
  ready_count: number;
  media: Media[];
}
export interface Media {
  id: string;
  tweet_id: string;
  kind: string;
  extension: string;
  status: string;
  error: string | null;
  size: number;
  width: number;
  height: number;
  duration: number | null;
  favorite: boolean;
  author: string;
  text: string;
  published_at: string;
  url: string;
  tags: string;
  has_file: boolean;
  has_thumbnail: boolean;
}
export interface Task {
  id: string;
  kind: string;
  target: string;
  label: string;
  status: string;
  progress: number;
  attempts: number;
  max_attempts: number;
  error: string | null;
  result: string | null;
  created_at: string;
  updated_at: string;
  available_at: string;
  parent_id: string | null;
  result_size: number;
  cookie_id: string | null;
}
export type ArchiveKind = "user" | "tweet" | "list" | "following";
export interface ArchiveTarget {
  kind: ArchiveKind;
  target: string;
}
export interface Schedule {
  id: string;
  name: string;
  enabled: boolean;
  interval_minutes: number;
  items: ArchiveTarget[];
  last_run_at: string | null;
  next_run_at: string;
  last_task_ids: string[];
}
export interface PoolCookie {
  id: string;
  name: string;
  enabled: boolean;
  kinds: string[];
  cooldown_until: string | null;
  error: string | null;
}
export interface FailedTweet {
  id: string;
  task_id: string;
  kind: string;
  target: string;
  tweet_id: string;
  label: string;
  error: string;
  status: string;
  attempts: number;
  max_attempts: number;
  available_at: string;
  updated_at: string;
}
export interface TaskGroup extends Task {
  download_total: number;
  download_completed: number;
  download_failed: number;
  download_cancelled: number;
  download_size: number;
  download_error: string | null;
}
export interface Snapshot {
  id: string;
  user_id: string;
  direction: string;
  status: string;
  expected_count: number | null;
  fetched_count: number;
  error: string | null;
  created_at: string;
}
export interface Member {
  id: string;
  username: string;
  name: string;
}
export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  limit: number;
}
export interface SettingsResponse {
  settings: Settings;
  cookie: CookieStatus;
  tools: Record<string, { available: boolean; version: string | null }>;
  storage: Storage;
}
export interface Dashboard {
  users: number;
  tweets: number;
  media: number;
  active_tasks: number;
  failed_tasks: number;
  storage: Storage;
  cookie: CookieStatus;
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api${path}`, {
    ...init,
    headers: {
      ...(init?.body instanceof FormData
        ? {}
        : { "Content-Type": "application/json" }),
      ...init?.headers,
    },
  });
  const data = await response
    .json()
    .catch(() => ({ error: `请求失败 (${response.status})` }));
  if (!response.ok) {
    if (response.status === 401 && path !== "/session")
      window.dispatchEvent(new Event("session-expired"));
    throw new Error(data.error || `请求失败 (${response.status})`);
  }
  return data as T;
}
export function send<T>(path: string, body?: unknown, method = "POST") {
  return request<T>(path, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}
export function params(
  values: Record<string, string | number | boolean | undefined>,
) {
  const query = new URLSearchParams();
  Object.entries(values).forEach(([key, value]) => {
    if (value !== undefined && value !== "") query.set(key, String(value));
  });
  return query.toString();
}
export function bytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 KB";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.max(
    1,
    Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4),
  );
  return `${Number((value / 1024 ** i).toFixed(i > 1 ? 1 : 2))} ${units[i]}`;
}
export function date(value: string | null, short = false) {
  if (!value) return "尚未同步";
  const d = new Date(value);
  return Number.isNaN(d.getTime())
    ? value
    : d.toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        ...(short ? {} : { year: "numeric" }),
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      });
}
