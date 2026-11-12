import { api, type AssetKind, type DownloadRecord, type Job, type TweetData } from "./api";

export interface ArchivedTweet extends TweetData {
  jobIds: number[]; bodyStatus: string; mediaStatus: string; mediaError?: string;
  downloads: DownloadRecord[]; archivedAt: string;
}
export interface TweetPage { items: ArchivedTweet[]; pagination: { page: number; pageSize: number; total: number; totalPages: number } }
export interface TweetSync { userId: string; lastId: string; cursor: string; headId: string; status: string; error: string; count: number; updatedAt: string }
export interface StorageTarget { id: string; name: string; type: "local" | "nas" | "s3"; version: number; root?: string; endpoint?: string; region?: string; bucket?: string; accessKey?: string; secretKey?: string; sessionToken?: string; accessKeyEnv?: string; secretKeyEnv?: string }
export interface AssetRule { targetId: string; prefix: string; directoryTemplate: string; filenameTemplate: string; directoryTemplateSet?: boolean; separateFolder?: boolean }
export interface StorageDocument { version: number; enabled: boolean; targets: StorageTarget[]; default: AssetRule; rules: Partial<Record<AssetKind, AssetRule>>; thumbnail: { width: number; height: number; format: string; quality: number } }
export interface Derivative { mediaKey: string; kind: AssetKind; assetId: string; status: string; error: string; jobId: number; updatedAt: string }
export const storageQueryKey = ["storage-document"];
export const tweetsQueryKey = ["tweet-archive"];
export const getStorage = async (signal?: AbortSignal) => {
  const document = await api<StorageDocument>("/api/storage", { signal });
  if (!document.default || !document.thumbnail || !Array.isArray(document.targets) || !document.rules) throw new Error("Invalid storage configuration response");
  return document;
};
export const saveStorage = (document: StorageDocument) => api<StorageDocument>("/api/storage", { method: "PUT", body: JSON.stringify(document) });
export const getTweets = (query: URLSearchParams, signal?: AbortSignal) => api<TweetPage>(`/api/archive/tweets?${query}`, { signal });
export const getTweetProgress = (signal?: AbortSignal) => api<{ items: TweetSync[] }>("/api/archive/progress", { signal });
export const retryTweetMedia = (id: string) => api<Job>(`/api/archive/tweets/${encodeURIComponent(id)}/retry-media`, { method: "POST" });
export const getDerivatives = (signal?: AbortSignal) => api<{ items: Derivative[] }>("/api/assets/derivatives/status", { signal });
export const regenerateDerivative = (item: Derivative) => api<Job>("/api/assets/derivatives/retry", { method: "POST", body: JSON.stringify({ kind: item.kind, mediaKey: item.mediaKey }) });
export const migrateStorage = () => api<Job>("/api/storage/migrate", { method: "POST" });
