import React, { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronUp, Copy, CornerUpLeft, ExternalLink, FileText, Play, RefreshCw, Repeat2 } from "lucide-react";
import type { DownloadRecord, Media, TweetData } from "../../lib/api";
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

export function TweetAvatar({ name, screenName, size = "default" }: { name?: string; screenName?: string; size?: "small" | "default" }) {
  const initial = (name || screenName || "X").trim().slice(0, 1).toUpperCase() || "X";
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

function MediaTile({ tweet, owner, record, overflow = 0, onOpen }: {
  tweet: TweetData;
  owner: { screenName: string; id: string };
  record: DownloadRecord;
  overflow?: number;
  onOpen: (record: DownloadRecord) => void;
}) {
  const type = recordType(tweet, record);
  const video = type === "video" || type === "animated_gif" || videoFile.test(record.filePath);
  const imageCandidates = useMemo(() => {
    const list: MediaSourceCandidate[] = [];
    const push = (candidate?: MediaSourceCandidate) => {
      if (candidate && !list.some(item => item.url === candidate.url)) {
        list.push(candidate);
      }
    };
    // 网格/卡片里优先用 X 图床的小图变体，失败再回退本地原文件。
    push(sanitizeMediaSrc(smallTwimgUrl(record.previewUrl)));
    push(sanitizeMediaSrc(record.fileUrl) ?? { url: `/api/library/downloads/${record.id}/file`, remote: false });
    return list;
  }, [record.fileUrl, record.id, record.previewUrl]);
  const [imageIndex, setImageIndex] = useState(0);
  useEffect(() => { setImageIndex(0); }, [record.fileUrl, record.id, record.previewUrl]);
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
    if (record.previewUrl && !videoFile.test(record.previewUrl.split(/[?#]/)[0])) {
      push(sanitizeMediaSrc(smallTwimgUrl(record.previewUrl) ?? record.previewUrl));
    }
    return list;
  }, [record.assetUrls?.thumbnail, record.id, record.previewUrl]);
  const [posterIndex, setPosterIndex] = useState(0);
  useEffect(() => { setPosterIndex(0); }, [record.id, record.assetUrls?.thumbnail, record.previewUrl]);
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
            alt={`${owner.screenName} · ${owner.id}`}
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
          alt={`${owner.screenName} · ${owner.id}`}
        />
      )}
      {video && <span className="tweet-play-icon" aria-hidden="true"><Play size={20} fill="currentColor" /></span>}
      {type === "animated_gif" && <span className="tweet-media-badge" aria-hidden="true">GIF</span>}
      {overflow > 0 && <span className="tweet-media-count" aria-hidden="true">+{overflow}</span>}
    </button>
  </div>;
}

function MediaGrid({ tweet, owner, records, compact, onOpen, onMore }: {
  tweet: TweetData;
  owner: { screenName: string; id: string };
  records: DownloadRecord[];
  compact: boolean;
  onOpen: (record: DownloadRecord) => void;
  onMore: () => void;
}) {
  const limit = compact ? 2 : 4;
  const shown = records.slice(0, limit);
  const hidden = records.length - shown.length;
  const count = Math.min(shown.length, 4);
  return <div className={`tweet-media tweet-media-count-${count}${shown.length > 1 ? " tweet-media-multiple" : ""}`}>
    {shown.map((record, index) => <MediaTile key={record.id} tweet={tweet} owner={owner} record={record} overflow={!compact && index === shown.length - 1 ? hidden : 0} onOpen={onOpen} />)}
    {compact && hidden > 0 && <button type="button" className="tweet-more-media" aria-label={t("archive.moreMedia", { count: hidden })} onClick={onMore}>+{hidden}</button>}
  </div>;
}

function QuoteCard({ quote, quoteDownloads, compact, highlight, onOpen }: {
  quote: TweetData;
  quoteDownloads: DownloadRecord[];
  compact: boolean;
  highlight: string;
  onOpen: (record: DownloadRecord) => void;
}) {
  const shown = quoteDownloads.slice(0, compact ? 2 : 3);
  const hidden = quoteDownloads.length - shown.length;
  return <div className="tweet-quote">
    <div className="tweet-quote-header">
      <TweetAvatar name={quote.author.name} screenName={quote.author.screenName} size="small" />
      <a className="tweet-quote-author" href={`/tweets?id=${encodeURIComponent(quote.id)}`}>
        <strong>{quote.author.name || quote.author.screenName}</strong>
        <span>@{quote.author.screenName}</span>
      </a>
      <time dateTime={quote.createdAt} title={formatDateTime(quote.createdAt)}>{formatTweetTime(quote.createdAt)}</time>
    </div>
    <TweetText text={quote.text} compact={compact} highlight={highlight} />
    {shown.length > 0 && <div className={`tweet-quote-media tweet-media-count-${Math.min(shown.length, 3)}`} data-count={shown.length}>
      {shown.map((record, index) => <MediaTile key={record.id} tweet={quote} owner={quote.author} record={record} overflow={index === shown.length - 1 ? hidden : 0} onOpen={onOpen} />)}
    </div>}
  </div>;
}

export const TweetCard = React.memo(function TweetCard({ tweet, compact, highlight, onFilterAuthor, onOpenDetail, onOpenMedia, onMoreMedia, onRetry, retryPending }: {
  tweet: ArchivedTweet;
  compact: boolean;
  highlight: string;
  onFilterAuthor: (screenName: string) => void;
  onOpenDetail: (tweetId: string) => void;
  onOpenMedia: (tweetId: string, record: DownloadRecord) => void;
  onMoreMedia: (tweetId: string) => void;
  onRetry: (id: string) => void;
  retryPending: boolean;
}) {
  useTranslation();
  const display = resolveTweetDisplay(tweet);
  const shown = display.tweet;
  const { main, byRelated } = splitRelatedDownloads(tweet.downloads, display.quotes);
  const owner = { screenName: shown.author.screenName, id: shown.id };
  const hasMedia = shown.media.length > 0 || display.quotes.some(q => q.media.length > 0);
  const retryable = hasMedia && !["completed", "not_requested"].includes(tweet.mediaStatus);
  const safeOriginalUrl = /^https?:\/\//i.test(shown.url ?? "") ? shown.url : undefined;
  return <article className="tweet-entry">
    {display.repostedBy && <a className="tweet-context" href={`/tweets?id=${encodeURIComponent(tweet.retweetedTweetId ?? "")}`}>
      <Repeat2 size={14} aria-hidden="true" />
      <span>{t("archive.repostedFrom", { author: display.repostedBy.screenName })}</span>
    </a>}
    <div className="tweet-layout">
      <div className="tweet-avatar"><TweetAvatar name={shown.author.name} screenName={shown.author.screenName} /></div>
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
            {display.quotes.map(quote => <QuoteCard key={quote.id} quote={quote} quoteDownloads={byRelated.get(quote.id) ?? []} compact={compact} highlight={highlight} onOpen={record => onOpenMedia(tweet.id, record)} />)}
            {display.missingQuoteId && <a className="tweet-note" href={`/tweets?id=${encodeURIComponent(display.missingQuoteId)}`}>{t("archive.quoteUnavailable", { id: display.missingQuoteId })}</a>}
            {shown.media.length > 0 && main.length === 0 && tweet.mediaStatus !== "not_requested" && <Tag tone="warning">{t("archive.mediaMissing")}</Tag>}
            {tweet.mediaError && <p className="tweet-media-error">{tweet.mediaError}</p>}
          </div>
          {main.length > 0 && <MediaGrid tweet={shown} owner={owner} records={main} compact={compact} onOpen={record => onOpenMedia(tweet.id, record)} onMore={() => onMoreMedia(tweet.id)} />}
        </div>
        <div className="tweet-toolbar" role="group" aria-label={t("archive.actions")}>
          <Tooltip content={t("查看归档详情")}><Button className="tweet-action" theme="borderless" type="tertiary" icon={<FileText size={16} />} aria-label={t("查看归档详情")} onClick={() => onOpenDetail(tweet.id)} /></Tooltip>
          <Tooltip content={t("复制正文")}><Button className="tweet-action" theme="borderless" type="tertiary" icon={<Copy size={16} />} aria-label={t("复制正文")} onClick={() => void copyToClipboard(shown.text, t("正文"))} /></Tooltip>
          {safeOriginalUrl ? <Tooltip content={t("archive.original")}><a className="tweet-action tweet-original semi-button semi-button-borderless semi-button-tertiary semi-button-with-icon-only" aria-label={t("archive.original")} href={safeOriginalUrl} target="_blank" rel="noreferrer"><ExternalLink size={16} /></a></Tooltip> : <Button className="tweet-action tweet-original" theme="borderless" type="tertiary" icon={<ExternalLink size={16} />} aria-label={t("archive.original")} disabled />}
          {retryable && <Button className="tweet-retry" size="small" theme="borderless" type="tertiary" icon={<RefreshCw size={14} />} loading={retryPending} disabled={retryPending} aria-busy={retryPending} onClick={() => onRetry(tweet.id)}>{t("archive.retryMedia")}</Button>}
          <div className="tweet-relations">{[["replyToId", "archive.reply"], ["quotedTweetId", "archive.quote"], ["retweetedTweetId", "archive.retweet"]].map(([key, label]) => tweet[key as "replyToId" | "quotedTweetId" | "retweetedTweetId"] ? <a key={key} href={`/tweets?id=${encodeURIComponent(tweet[key as "replyToId"]!)}`}>{t(label)} {tweet[key as "replyToId"]}</a> : null)}</div>
          <div className="tweet-archive-meta"><time dateTime={tweet.archivedAt}>{t("归档于")} {formatDateTime(tweet.archivedAt)}</time><span>{tweet.downloads.length} {t("个文件")}</span>{tweet.jobIds.length > 0 && <a href={`/tasks?q=${tweet.jobIds[0]}`}>{t("来源任务")} #{tweet.jobIds[0]}</a>}</div>
        </div>
      </div>
    </div>
  </article>;
});
