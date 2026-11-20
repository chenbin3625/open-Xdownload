import type { DownloadRecord, Media, TweetData } from "./api";
import i18n, { t } from "./i18n";

/** 正文实体：链接、@用户名、#话题，其余按纯文本渲染。 */
export type TweetTextToken =
  | { type: "text"; value: string }
  | { type: "url" | "mention" | "hashtag"; value: string; href: string };

// 一次扫描出所有可能实体；边界与尾随标点在下面逐条校验。
const entityPattern = /(https?:\/\/[^\s<>"'`]+|@\w{1,15}|#[\p{L}\p{M}\p{N}_]+)/giu;
const trailingPunctuation = /[.,;:!?)\]}>'"」』】、。，；：！？…]+$/u;

function entityHref(value: string) {
  if (/^https?:\/\//i.test(value)) return value;
  if (value.startsWith("@")) return `https://x.com/${value.slice(1)}`;
  return `https://x.com/hashtag/${encodeURIComponent(value.slice(1))}`;
}

/**
 * 把推文正文拆成实体片段。X 只把合法 @用户名与话题渲染成链接：
 * 邮箱中间的 @ 不算，纯数字话题不算，链接末尾的标点不并入链接。
 */
export function tokenizeTweetText(text: string): TweetTextToken[] {
  if (!text) return [];
  const tokens: TweetTextToken[] = [];
  let cursor = 0;
  for (const match of text.matchAll(entityPattern)) {
    const raw = match[0];
    const start = match.index ?? 0;
    const before = start > 0 ? text[start - 1] : "";
    const after = text[start + raw.length] ?? "";
    let value = raw;
    if (/^https?:\/\//i.test(raw)) {
      value = raw.replace(trailingPunctuation, "");
    } else if (raw.startsWith("@")) {
      if (/[\w@]/u.test(before) || /\w/u.test(after)) continue;
    } else {
      if (/[\w&#]/u.test(before)) continue;
      if (!/[\p{L}\p{M}]/u.test(raw.slice(1))) continue;
    }
    if (!value) continue;
    if (start > cursor) tokens.push({ type: "text", value: text.slice(cursor, start) });
    tokens.push({ type: raw.startsWith("http") ? "url" : raw.startsWith("@") ? "mention" : "hashtag", value, href: entityHref(value) });
    cursor = start + value.length;
  }
  if (cursor < text.length) tokens.push({ type: "text", value: text.slice(cursor) });
  return tokens;
}

/** 搜索命中时逐片段高亮，保证与链接实体互不破坏。 */
export function highlightToken(value: string, highlight: string) {
  const needle = highlight.trim();
  if (!needle) return [value];
  return value.split(new RegExp(`(${needle.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")})`, "ig"));
}

const relativeFormatters = new Map<string, Intl.DateTimeFormat>();
function dateFormatter(language: string, withYear: boolean) {
  const key = `${language}:${withYear}`;
  let formatter = relativeFormatters.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat(language, withYear ? { year: "numeric", month: "short", day: "numeric" } : { month: "short", day: "numeric" });
    relativeFormatters.set(key, formatter);
  }
  return formatter;
}

/**
 * X 时间线的短时间：一天内按秒/分/小时，当年只显示月日，跨年补年份。
 * 完整时间留在 time[title] 与详情抽屉里。
 */
export function formatTweetTime(value: string, now = Date.now()) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const diff = Math.max(0, now - date.getTime());
  if (diff < 60_000) return t("archive.secondsAgo", { count: Math.max(1, Math.floor(diff / 1000)) });
  if (diff < 3_600_000) return t("archive.minutesAgo", { count: Math.floor(diff / 60_000) });
  if (diff < 86_400_000) return t("archive.hoursAgo", { count: Math.floor(diff / 3_600_000) });
  const language = i18n.resolvedLanguage === "en" ? "en" : "zh-CN";
  return dateFormatter(language, date.getFullYear() !== new Date(now).getFullYear()).format(date);
}

const videoHost = /(^|\.)video\.twimg\.com$/i;
const photoHost = /(^|\.)pbs\.twimg\.com$/i;

/** 与后端 MediaIdentity 对齐的媒体身份：忽略 tag/尺寸参数与视频清晰度变体。 */
export function mediaKey(rawURL: string) {
  try {
    const parsed = new URL(rawURL);
    const path = parsed.pathname;
    if (videoHost.test(parsed.hostname)) {
      const segments = path.replace(/^\//, "").split("/");
      if (segments.length >= 3 && (segments[0] === "ext_tw_video" || segments[0] === "amplify_video") && /^\d+$/.test(segments[1])) {
        return `${segments[0]}/${segments[1]}`;
      }
    }
    if (photoHost.test(parsed.hostname) && path.startsWith("/media/")) {
      const id = path.slice("/media/".length).replace(/\.[a-z0-9]+$/i, "");
      if (id) return `media/${id}`;
    }
    return `${parsed.hostname.toLowerCase()}${path}`;
  } catch {
    return rawURL;
  }
}

function mediaKeys(media: Media) {
  const keys = new Set<string>();
  for (const url of [media.bestUrl, media.url, ...(media.variants ?? []).map(variant => variant.url)]) {
    if (url) keys.add(mediaKey(url));
  }
  return keys;
}

/**
 * 引用推文的媒体也被下载到外层推文名下（开启“归档嵌套媒体”时），
 * 这里按媒体身份把它们分回引用卡片，其余留在主媒体区。
 */
export function splitRelatedDownloads(downloads: DownloadRecord[], related: TweetData[]) {
  const used = new Set<number>();
  const byRelated = new Map<string, DownloadRecord[]>();
  for (const item of related) {
    const keys = new Set(item.media.flatMap(media => [...mediaKeys(media)]));
    if (!keys.size) continue;
    const records = downloads.filter(record => !used.has(record.id) && keys.has(mediaKey(record.mediaUrl)));
    records.forEach(record => used.add(record.id));
    if (records.length) byRelated.set(item.id, records);
  }
  return { main: downloads.filter(record => !used.has(record.id)), byRelated };
}

/** 找到下载记录对应的原始媒体，用来区分视频/动图与普通图片。 */
export function mediaOfRecord(tweet: TweetData, record: DownloadRecord): Media | undefined {
  const key = mediaKey(record.mediaUrl);
  return tweet.media.find(media => mediaKeys(media).has(key));
}

export interface TweetDisplay {
  /** 实际展示正文的作者：转推时是原推作者。 */
  tweet: TweetData;
  repostedBy?: { name: string; screenName: string };
  replyTo?: TweetData;
  replyToId?: string;
  quotes: TweetData[];
  missingQuoteId?: string;
  missingRepostId?: string;
}

function hasArchivedContent(item: TweetData): boolean {
  return Boolean(item.text || (Array.isArray(item.media) && item.media.length > 0) || item.author?.screenName);
}

/** 把归档记录解析成 X 时间线的展示结构：原推、转推来源、回复父级与引用卡片。 */
export function resolveTweetDisplay(tweet: TweetData): TweetDisplay {
  const related = Array.isArray(tweet.related) ? tweet.related : [];
  const original = tweet.retweetedTweetId ? related.find(item => item.id === tweet.retweetedTweetId && hasArchivedContent(item)) : undefined;
  const shown = original ?? tweet;
  const pool: TweetData[] = [];
  const seen = new Set<string>([shown.id]);
  for (const item of [...(Array.isArray(shown.related) ? shown.related : []), ...related]) {
    if (!item?.id || seen.has(item.id)) continue;
    seen.add(item.id);
    pool.push(item);
  }
  const replyToId = shown.replyToId || tweet.replyToId;
  const replyTo = replyToId ? pool.find(item => item.id === replyToId && hasArchivedContent(item)) : undefined;
  const quoteId = shown.quotedTweetId || tweet.quotedTweetId;
  const quote = quoteId && quoteId !== shown.id ? pool.find(item => item.id === quoteId && hasArchivedContent(item)) : undefined;
  const quotes: TweetData[] = [];
  if (quote) quotes.push(quote);
  for (const item of pool) {
    if (!hasArchivedContent(item) || item.id === replyTo?.id || quotes.some(existing => existing.id === item.id)) continue;
    quotes.push(item);
  }
  return {
    tweet: shown,
    repostedBy: original ? { name: tweet.author.name || tweet.author.screenName, screenName: tweet.author.screenName } : undefined,
    replyTo,
    replyToId: replyToId && !replyTo ? replyToId : undefined,
    quotes,
    missingQuoteId: quoteId && quoteId !== shown.id && !quote ? quoteId : undefined,
    missingRepostId: tweet.retweetedTweetId && !original ? tweet.retweetedTweetId : undefined,
  };
}
