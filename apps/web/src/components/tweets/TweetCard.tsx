import React, { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  Bookmark,
  BookOpen,
  Check,
  ChevronDown,
  ChevronUp,
  Copy,
  CornerUpLeft,
  ExternalLink,
  Eye,
  FileText,
  Heart,
  MessageCircle,
  MessageSquareText,
  Play,
  RefreshCw,
  Repeat2,
  Star,
  Tag as TagIcon,
  Trash2,
} from "lucide-react";
import type { DownloadRecord, Media, TweetArticle, TweetData, TweetMetrics, TweetPoll } from "../../lib/api";
import type { ArchivedTweet } from "../../lib/archive";
import { t, useTranslation } from "../../lib/i18n";
import { copyToClipboard, formatDateTime } from "../common/CommonUI";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { Avatar, Tag } from "../semi/data";
import { Tooltip } from "../semi/overlays";
import { formatTweetTime, highlightToken, mediaOfRecord, resolveTweetDisplay, smallTwimgUrl, splitRelatedDownloads, tokenizeTweetText } from "../../lib/tweetDisplay";

const avatarColors = ["blue", "cyan", "green", "indigo", "orange", "pink", "purple", "teal", "violet", "amber"] as const;
function avatarColor(seed: string) {
  let hash = 0;
  for (const char of seed) hash = (hash * 31 + (char.codePointAt(0) ?? 0)) >>> 0;
  return avatarColors[hash % avatarColors.length];
}

function isSafeHttpUrl(raw?: string): boolean {
  if (!raw) return false;
  try {
    const parsed = new URL(raw);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

export function TweetAvatar({
  name,
  screenName,
  avatarUrl,
  size = "default",
}: {
  name?: string;
  screenName?: string;
  avatarUrl?: string;
  size?: "small" | "default";
}) {
  const initial = (name || screenName || "X").trim().slice(0, 1).toUpperCase() || "X";
  const candidates = useMemo(() => {
    const list: { url: string; remote: boolean }[] = [];
    if (screenName && avatarUrl) {
      list.push({ url: `/api/avatars/${encodeURIComponent(screenName)}`, remote: false });
    }
    if (isSafeHttpUrl(avatarUrl)) {
      list.push({ url: avatarUrl!, remote: true });
    }
    return list;
  }, [screenName, avatarUrl]);
  const [srcIndex, setSrcIndex] = useState(0);
  useEffect(() => { setSrcIndex(0); }, [screenName, avatarUrl]);

  if (srcIndex < candidates.length) {
    const current = candidates[srcIndex];
    const sizeClass = size === "small" ? "size-5" : "size-10";
    return (
      <img
        src={current.url}
        alt=""
        loading="lazy"
        decoding="async"
        referrerPolicy={current.remote ? "no-referrer" : undefined}
        onError={() => setSrcIndex((idx) => idx + 1)}
        className={`${sizeClass} shrink-0 rounded-full border border-line object-cover`}
      />
    );
  }
  return <Avatar size={size} color={avatarColor(screenName || name || "x")}>{initial}</Avatar>;
}

function Highlighted({ value, highlight }: { value: string; highlight: string }) {
  const parts = highlightToken(value, highlight);
  return <>{parts.map((part, index) => (index % 2 === 1 && part ? <mark key={index}>{part}</mark> : part))}</>;
}

/** 正文：@、#、链接按 X 的方式成为实体，其余文本原样保留。 */
export function TweetText({ text, compact, highlight = "" }: { text: string; compact: boolean; highlight?: string }) {
  useTranslation();
  const body = useRef<HTMLParagraphElement>(null);
  const id = useId();
  const [expanded, setExpanded] = useState(false);
  const [clipped, setClipped] = useState(false);
  useEffect(() => { setExpanded(false); }, [text, compact]);
  useLayoutEffect(() => {
    const node = body.current;
    if (!node || !compact || expanded) return;
    const measure = () => setClipped(node.scrollHeight > node.clientHeight + 1);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, [text, compact, expanded]);
  const tokens = useMemo(() => tokenizeTweetText(text), [text]);
  return <div className="tweet-text">
    <p id={id} ref={body} className={`tweet-body${compact && !expanded ? " tweet-body-clamped" : ""}`}>
      {tokens.map((token, index) => token.type === "text"
        ? <React.Fragment key={index}><Highlighted value={token.value} highlight={highlight} /></React.Fragment>
        : <a key={index} className={`tweet-entity tweet-entity-${token.type}`} href={token.href} target="_blank" rel="noreferrer"><Highlighted value={token.value} highlight={highlight} /></a>)}
    </p>
    {compact && (clipped || expanded) && <Button className="tweet-expand" size="small" theme="borderless" icon={expanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />} aria-expanded={expanded} aria-controls={id} onClick={() => setExpanded(!expanded)}>{t(expanded ? "archive.collapseText" : "archive.expandText")}</Button>}
  </div>;
}

function ArticleCard({ article, compact }: { article: TweetArticle; compact: boolean }) {
  useTranslation();
  const [expanded, setExpanded] = useState(false);
  const bodyText = article.plainText || article.previewText || "";
  if (!article.title && !bodyText) return null;
  return (
    <div className="mt-2 rounded-card border border-line bg-surface-muted/40 p-3 space-y-1.5 text-xs">
      <div className="flex items-center gap-1.5 text-brand-600 font-semibold">
        <BookOpen size={14} aria-hidden="true" />
        <span>{t("X 长文章")}</span>
      </div>
      {article.title && <div className="text-sm font-semibold text-fg">{article.title}</div>}
      {bodyText && (
        <>
          <p className={`whitespace-pre-wrap break-words text-fg-body ${compact && !expanded ? "line-clamp-4" : ""}`}>
            {bodyText}
          </p>
          {compact && bodyText.length > 220 && (
            <Button
              size="small"
              theme="borderless"
              onClick={() => setExpanded((v) => !v)}
            >
              {expanded ? t("收起文章") : t("展开全文")}
            </Button>
          )}
        </>
      )}
    </div>
  );
}

function PollCard({ poll }: { poll: TweetPoll }) {
  useTranslation();
  if (!poll.choices?.length) return null;
  const total = poll.totalVotes || poll.choices.reduce((sum, item) => sum + (item.votes || 0), 0);
  return (
    <div className="mt-2 rounded-card border border-line bg-surface-muted/40 p-3 space-y-2 text-xs">
      {poll.choices.map((choice, idx) => {
        const pct = total > 0 ? Math.round((choice.votes / total) * 100) : 0;
        return (
          <div key={idx} className="space-y-0.5">
            <div className="flex items-center justify-between gap-2">
              <span className="font-medium text-fg">{choice.label}</span>
              <span className="shrink-0 text-fg-muted tabular-nums">
                {pct}% ({choice.votes.toLocaleString()})
              </span>
            </div>
            <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-muted">
              <div className="h-full rounded-full bg-brand-500" style={{ width: `${pct}%` }} />
            </div>
          </div>
        );
      })}
      <div className="flex flex-wrap items-center gap-2 text-fg-subtle tabular-nums">
        <span>{t("共 {{value0}} 票", { value0: total.toLocaleString() })}</span>
        {poll.votingStatus === "true" && <span>· {t("已结束")}</span>}
      </div>
    </div>
  );
}

function formatMetricCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 10_000) return `${(n / 1_000).toFixed(1)}K`;
  return n.toLocaleString();
}

function MetricsRow({ metrics }: { metrics: TweetMetrics }) {
  const items: { icon: React.ReactNode; value: number; label: string }[] = [];
  if (metrics.replies) items.push({ icon: <MessageCircle size={13} aria-hidden="true" />, value: metrics.replies, label: t("回复") });
  if (metrics.retweets) items.push({ icon: <Repeat2 size={13} aria-hidden="true" />, value: metrics.retweets, label: t("转发") });
  if (metrics.likes) items.push({ icon: <Heart size={13} aria-hidden="true" />, value: metrics.likes, label: t("点赞") });
  if (metrics.bookmarks) items.push({ icon: <Bookmark size={13} aria-hidden="true" />, value: metrics.bookmarks, label: t("书签") });
  if (metrics.views) items.push({ icon: <Eye size={13} aria-hidden="true" />, value: metrics.views, label: t("浏览") });
  if (items.length === 0) return null;
  return (
    <div className="mt-2 flex flex-wrap items-center gap-3 text-xs text-fg-subtle tabular-nums">
      {items.map((item, idx) => (
        <span key={idx} className="inline-flex items-center gap-1" title={`${item.label}: ${item.value.toLocaleString()}`}>
          {item.icon}
          <span>{formatMetricCount(item.value)}</span>
        </span>
      ))}
    </div>
  );
}

const videoFile = /\.(mp4|webm|mov|m4v|ogv)$/i;

interface MediaSourceCandidate {
  url: string;
  remote: boolean;
}

function sanitizeMediaSrc(raw: string | null | undefined): MediaSourceCandidate | undefined {
  const trimmed = raw?.trim();
  if (!trimmed) return undefined;
  if (trimmed.startsWith("/") && !trimmed.startsWith("//")) {
    return { url: trimmed, remote: false };
  }
  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol === "http:" || parsed.protocol === "https:") {
      return { url: parsed.toString(), remote: true };
    }
  } catch {
    return undefined;
  }
  return undefined;
}

function recordType(tweet: TweetData, record: DownloadRecord): Media["type"] {
  return mediaOfRecord(tweet, record)?.type ?? (videoFile.test(record.filePath) ? "video" : "photo");
}

function MediaTile({ tweet, owner, record, overflow = 0, offlineThumbnails = false, onOpen }: {
  tweet: TweetData;
  owner: { screenName: string; id: string };
  record: DownloadRecord;
  overflow?: number;
  offlineThumbnails?: boolean;
  onOpen: (record: DownloadRecord) => void;
}) {
  const mediaMeta = mediaOfRecord(tweet, record);
  const type = mediaMeta?.type ?? (videoFile.test(record.filePath) ? "video" : "photo");
  const altText = mediaMeta?.altText?.trim() || "";
  const video = type === "video" || type === "animated_gif" || videoFile.test(record.filePath);
  const imageCandidates = useMemo(() => {
    const list: MediaSourceCandidate[] = [];
    const push = (candidate?: MediaSourceCandidate) => {
      if (candidate && !list.some(item => item.url === candidate.url)) {
        list.push(candidate);
      }
    };
    if (offlineThumbnails && record.id > 0) {
      push({ url: `/api/library/downloads/${record.id}/thumbnail`, remote: false });
      push(sanitizeMediaSrc(record.fileUrl) ?? { url: `/api/library/downloads/${record.id}/file`, remote: false });
    } else {
      // 网格/卡片里优先用 X 图床的小图变体，失败再回退本地原文件。
      push(sanitizeMediaSrc(smallTwimgUrl(record.previewUrl)));
      push(sanitizeMediaSrc(record.fileUrl) ?? { url: `/api/library/downloads/${record.id}/file`, remote: false });
    }
    return list;
  }, [offlineThumbnails, record.fileUrl, record.id, record.previewUrl]);
  const [imageIndex, setImageIndex] = useState(0);
  useEffect(() => { setImageIndex(0); }, [offlineThumbnails, record.fileUrl, record.id, record.previewUrl]);
  const currentImage = imageIndex < imageCandidates.length ? imageCandidates[imageIndex] : imageCandidates[imageCandidates.length - 1];
  const posterCandidates = useMemo(() => {
    const list: MediaSourceCandidate[] = [];
    const push = (candidate?: MediaSourceCandidate) => {
      if (candidate && !list.some(item => item.url === candidate.url)) {
        list.push(candidate);
      }
    };
    push(sanitizeMediaSrc(record.assetUrls?.thumbnail));
    if (record.id > 0) {
      push({ url: `/api/library/downloads/${record.id}/preview`, remote: false });
    }
    if (!offlineThumbnails && record.previewUrl && !videoFile.test(record.previewUrl.split(/[?#]/)[0])) {
      push(sanitizeMediaSrc(smallTwimgUrl(record.previewUrl) ?? record.previewUrl));
    }
    return list;
  }, [offlineThumbnails, record.assetUrls?.thumbnail, record.id, record.previewUrl]);
  const [posterIndex, setPosterIndex] = useState(0);
  useEffect(() => { setPosterIndex(0); }, [offlineThumbnails, record.id, record.assetUrls?.thumbnail, record.previewUrl]);
  const currentPoster = posterIndex < posterCandidates.length ? posterCandidates[posterIndex] : undefined;
  const label = `${video ? t("播放视频") : t("archive.openImage")} · @${owner.screenName} · ${owner.id} · ${record.id}`;
  return <div className="tweet-media-item">
    <button type="button" aria-label={label} onClick={() => onOpen(record)}>
      {video ? (
        currentPoster ? (
          <img
            src={currentPoster.url}
            loading="lazy"
            referrerPolicy={currentPoster.remote ? "no-referrer" : undefined}
            onError={() => setPosterIndex(current => current + 1)}
            alt={altText || `${owner.screenName} · ${owner.id}`}
          />
        ) : (
          <span className="flex h-full w-full items-center justify-center bg-media-bg" aria-hidden="true" />
        )
      ) : (
        <img
          src={currentImage.url}
          loading="lazy"
          decoding="async"
          referrerPolicy={currentImage.remote ? "no-referrer" : undefined}
          onError={() => setImageIndex(current => current + 1 < imageCandidates.length ? current + 1 : current)}
          alt={altText || `${owner.screenName} · ${owner.id}`}
        />
      )}
      {video && <span className="tweet-play-icon" aria-hidden="true"><Play size={20} fill="currentColor" /></span>}
      {type === "animated_gif" && <span className="tweet-media-badge" aria-hidden="true">GIF</span>}
      {altText && <span className="tweet-media-badge" title={altText}>ALT</span>}
      {overflow > 0 && <span className="tweet-media-count" aria-hidden="true">+{overflow}</span>}
    </button>
  </div>;
}

function MediaGrid({ tweet, owner, records, compact, offlineThumbnails, onOpen, onMore }: {
  tweet: TweetData;
  owner: { screenName: string; id: string };
  records: DownloadRecord[];
  compact: boolean;
  offlineThumbnails?: boolean;
  onOpen: (record: DownloadRecord) => void;
  onMore: () => void;
}) {
  const limit = compact ? 2 : 4;
  const shown = records.slice(0, limit);
  const hidden = records.length - shown.length;
  const count = Math.min(shown.length, 4);
  return <div className={`tweet-media tweet-media-count-${count}${shown.length > 1 ? " tweet-media-multiple" : ""}`}>
    {shown.map((record, index) => <MediaTile key={record.id} tweet={tweet} owner={owner} record={record} overflow={!compact && index === shown.length - 1 ? hidden : 0} offlineThumbnails={offlineThumbnails} onOpen={onOpen} />)}
    {compact && hidden > 0 && <button type="button" className="tweet-more-media" aria-label={t("archive.moreMedia", { count: hidden })} onClick={onMore}>+{hidden}</button>}
  </div>;
}

function QuoteCard({ quote, quoteDownloads, compact, highlight, offlineThumbnails, onOpen }: {
  quote: TweetData;
  quoteDownloads: DownloadRecord[];
  compact: boolean;
  highlight: string;
  offlineThumbnails?: boolean;
  onOpen: (record: DownloadRecord) => void;
}) {
  const shown = quoteDownloads.slice(0, compact ? 2 : 3);
  const hidden = quoteDownloads.length - shown.length;
  return <div className="tweet-quote">
    <div className="tweet-quote-header">
      <TweetAvatar name={quote.author.name} screenName={quote.author.screenName} avatarUrl={quote.author.avatarUrl} size="small" />
      <a className="tweet-quote-author" href={`/tweets?id=${encodeURIComponent(quote.id)}`}>
        <strong>{quote.author.name || quote.author.screenName}</strong>
        <span>@{quote.author.screenName}</span>
      </a>
      <time dateTime={quote.createdAt} title={formatDateTime(quote.createdAt)}>{formatTweetTime(quote.createdAt)}</time>
    </div>
    <TweetText text={quote.text} compact={compact} highlight={highlight} />
    {shown.length > 0 && <div className={`tweet-quote-media tweet-media-count-${Math.min(shown.length, 3)}`} data-count={shown.length}>
      {shown.map((record, index) => <MediaTile key={record.id} tweet={quote} owner={quote.author} record={record} overflow={index === shown.length - 1 ? hidden : 0} offlineThumbnails={offlineThumbnails} onOpen={onOpen} />)}
    </div>}
  </div>;
}

export const TweetCard = React.memo(function TweetCard({
  tweet,
  compact,
  highlight,
  offlineThumbnails = false,
  onFilterAuthor,
  onOpenDetail,
  onOpenMedia,
  onMoreMedia,
  onRetry,
  onToggleStar,
  onOpenOrganize,
  onOpenThread,
  onRequestDelete,
  retryPending,
}: {
  tweet: ArchivedTweet;
  compact: boolean;
  highlight: string;
  offlineThumbnails?: boolean;
  onFilterAuthor: (screenName: string) => void;
  onOpenDetail: (tweetId: string) => void;
  onOpenMedia: (tweetId: string, record: DownloadRecord) => void;
  onMoreMedia: (tweetId: string) => void;
  onRetry: (id: string) => void;
  onToggleStar?: (tweetId: string, nextStarred: boolean) => void;
  onOpenOrganize?: (tweetId: string) => void;
  onOpenThread?: (tweetId: string) => void;
  onRequestDelete?: (tweetId: string) => void;
  retryPending: boolean;
}) {
  useTranslation();
  const [copiedText, setCopiedText] = useState(false);
  const copiedTimerRef = useRef<number | null>(null);
  useEffect(() => () => {
    if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current);
  }, []);
  const display = resolveTweetDisplay(tweet);
  const shown = display.tweet;
  const { main, byRelated } = splitRelatedDownloads(tweet.downloads, display.quotes);
  const owner = { screenName: shown.author.screenName, id: shown.id };
  const hasMedia = shown.media.length > 0 || display.quotes.some(q => q.media.length > 0);
  const retryable = hasMedia && !["completed", "not_requested"].includes(tweet.mediaStatus);
  const safeOriginalUrl = /^https?:\/\//i.test(shown.url ?? "") ? shown.url : undefined;
  const handleCopyText = () => {
    void copyToClipboard(shown.text, t("正文")).then(() => {
      setCopiedText(true);
      if (copiedTimerRef.current !== null) window.clearTimeout(copiedTimerRef.current);
      copiedTimerRef.current = window.setTimeout(() => {
        copiedTimerRef.current = null;
        setCopiedText(false);
      }, 1500);
    });
  };
  return <article className="tweet-entry">
    {display.repostedBy && <a className="tweet-context" href={`/tweets?id=${encodeURIComponent(tweet.retweetedTweetId ?? "")}`}>
      <Repeat2 size={14} aria-hidden="true" />
      <span>{t("archive.repostedFrom", { author: display.repostedBy.screenName })}</span>
    </a>}
    <div className="tweet-layout">
      <div className="tweet-avatar"><TweetAvatar name={shown.author.name} screenName={shown.author.screenName} avatarUrl={shown.author.avatarUrl} /></div>
      <div className="tweet-main">
        <header className="tweet-header">
          <div className="tweet-identity">
            <strong className="tweet-name">{shown.author.name || shown.author.screenName}</strong>
            <button type="button" className="tweet-author" title={t("筛选此作者")} onClick={() => onFilterAuthor(shown.author.screenName)}>@{shown.author.screenName}</button>
            <span className="tweet-separator" aria-hidden="true">·</span>
            <time dateTime={shown.createdAt} title={formatDateTime(shown.createdAt)}>{formatTweetTime(shown.createdAt)}</time>
          </div>
        </header>
        {display.replyTo ? <a className="tweet-reply-context" href={`/tweets?id=${encodeURIComponent(display.replyTo.id)}`}><CornerUpLeft size={14} aria-hidden="true" /><span>{t("archive.replyingTo", { author: display.replyTo.author.screenName })}</span></a>
          : display.replyToId ? <a className="tweet-reply-context" href={`/tweets?id=${encodeURIComponent(display.replyToId)}`}><CornerUpLeft size={14} aria-hidden="true" /><span>{t("archive.replyingToUnknown")}</span></a> : null}
        <div className={`tweet-content${main.length ? " tweet-has-media" : ""}`}>
          <div className="tweet-copy">
            {display.missingRepostId && <a className="tweet-note" href={`/tweets?id=${encodeURIComponent(display.missingRepostId)}`}>{t("archive.retweetUnavailable", { id: display.missingRepostId })}</a>}
            <TweetText text={shown.text} compact={compact} highlight={highlight} />
            {shown.article && <ArticleCard article={shown.article} compact={compact} />}
            {shown.poll && <PollCard poll={shown.poll} />}
            {display.quotes.map(quote => <QuoteCard key={quote.id} quote={quote} quoteDownloads={byRelated.get(quote.id) ?? []} compact={compact} highlight={highlight} offlineThumbnails={offlineThumbnails} onOpen={record => onOpenMedia(tweet.id, record)} />)}
            {display.missingQuoteId && <a className="tweet-note" href={`/tweets?id=${encodeURIComponent(display.missingQuoteId)}`}>{t("archive.quoteUnavailable", { id: display.missingQuoteId })}</a>}
            {shown.media.length > 0 && main.length === 0 && tweet.mediaStatus !== "not_requested" && <Tag tone="warning">{t("archive.mediaMissing")}</Tag>}
            {tweet.mediaError && <p className="tweet-media-error">{tweet.mediaError}</p>}
            {shown.metrics && <MetricsRow metrics={shown.metrics} />}
            {tweet.tags && tweet.tags.length > 0 && (
              <div className="mt-1.5 flex flex-wrap gap-1">
                {tweet.tags.map((tg) => (
                  <Tag key={tg.id} size="sm">{tg.name}</Tag>
                ))}
              </div>
            )}
          </div>
          {main.length > 0 && <MediaGrid tweet={shown} owner={owner} records={main} compact={compact} offlineThumbnails={offlineThumbnails} onOpen={record => onOpenMedia(tweet.id, record)} onMore={() => onMoreMedia(tweet.id)} />}
        </div>
        <div className="tweet-toolbar" role="group" aria-label={t("archive.actions")}>
          {onToggleStar && (
            <Tooltip content={tweet.starred ? t("取消收藏") : t("收藏推文")}>
              <Button
                className="tweet-action"
                theme="borderless"
                type={tweet.starred ? "warning" : "tertiary"}
                icon={<Star size={16} fill={tweet.starred ? "currentColor" : "none"} />}
                aria-label={tweet.starred ? t("取消收藏") : t("收藏推文")}
                aria-pressed={Boolean(tweet.starred)}
                onClick={() => onToggleStar(tweet.id, !tweet.starred)}
              />
            </Tooltip>
          )}
          {onOpenOrganize && (
            <Tooltip content={t("标签与合集")}>
              <Button
                className="tweet-action"
                theme="borderless"
                type="tertiary"
                icon={<TagIcon size={16} />}
                aria-label={t("设置标签与合集")}
                onClick={() => onOpenOrganize(tweet.id)}
              />
            </Tooltip>
          )}
          {onOpenThread && (tweet.conversationId || tweet.replyToId) && (
            <Tooltip content={t("查看长串对话")}>
              <Button
                className="tweet-action"
                theme="borderless"
                type="tertiary"
                icon={<MessageSquareText size={16} />}
                aria-label={t("查看长串对话")}
                onClick={() => onOpenThread(tweet.id)}
              />
            </Tooltip>
          )}
          <Tooltip content={t("查看归档详情")}><Button className="tweet-action" theme="borderless" type="tertiary" icon={<FileText size={16} />} aria-label={t("查看归档详情")} onClick={() => onOpenDetail(tweet.id)} /></Tooltip>
          <Tooltip content={t("复制正文")}><Button className="tweet-action" theme="borderless" type="tertiary" icon={copiedText ? <Check size={16} className="text-success" /> : <Copy size={16} />} aria-label={t("复制正文")} onClick={handleCopyText} /></Tooltip>
          {safeOriginalUrl ? <Tooltip content={t("archive.original")}><a className="tweet-action tweet-original semi-button semi-button-borderless semi-button-tertiary semi-button-with-icon-only" aria-label={t("archive.original")} href={safeOriginalUrl} target="_blank" rel="noreferrer"><ExternalLink size={16} /></a></Tooltip> : <Button className="tweet-action tweet-original" theme="borderless" type="tertiary" icon={<ExternalLink size={16} />} aria-label={t("archive.original")} disabled />}
          {onRequestDelete && (
            <Tooltip content={t("删除归档推文")}>
              <Button
                className="tweet-action"
                theme="borderless"
                type="danger"
                icon={<Trash2 size={16} />}
                aria-label={t("删除归档推文")}
                onClick={() => onRequestDelete(tweet.id)}
              />
            </Tooltip>
          )}
          {retryable && <Button className="tweet-retry" size="small" theme="borderless" type="tertiary" icon={<RefreshCw size={14} />} loading={retryPending} disabled={retryPending} aria-busy={retryPending} onClick={() => onRetry(tweet.id)}>{t("archive.retryMedia")}</Button>}
          <div className="tweet-relations">{[["replyToId", "archive.reply"], ["quotedTweetId", "archive.quote"], ["retweetedTweetId", "archive.retweet"]].map(([key, label]) => tweet[key as "replyToId" | "quotedTweetId" | "retweetedTweetId"] ? <a key={key} href={`/tweets?id=${encodeURIComponent(tweet[key as "replyToId"]!)}`}>{t(label)} {tweet[key as "replyToId"]}</a> : null)}</div>
          <div className="tweet-archive-meta"><time dateTime={tweet.archivedAt}>{t("归档于")} {formatDateTime(tweet.archivedAt)}</time><span>{tweet.downloads.length} {t("个文件")}</span>{tweet.jobIds.length > 0 && <a href={`/tasks?q=${tweet.jobIds[0]}`}>{t("来源任务")} #{tweet.jobIds[0]}</a>}</div>
        </div>
      </div>
    </div>
  </article>;
});
