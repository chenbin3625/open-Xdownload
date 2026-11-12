import React, { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlignJustify, CalendarDays, ChevronDown, ChevronUp, ExternalLink, RefreshCw, Repeat2, Rows3, Search } from "lucide-react";
import { Button, DatePicker, Input, RadioGroup, Select } from "@douyinfe/semi-ui";
import { getTweets, retryTweetMedia, tweetsQueryKey, type ArchivedTweet } from "../lib/archive";
import { t, useTranslation } from "../lib/i18n";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import { formatDateTime } from "../components/common/CommonUI";
import { Alert, Avatar, Empty, ListSkeleton, MediaModal, Pagination, Tag, Tooltip, toast } from "../components/semi";

function readFilters() { return new URLSearchParams(window.location.search); }

function TweetText({ text, compact }: { text: string; compact: boolean }) {
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
  return <div className="tweet-text">
    <p id={id} ref={body} className={`tweet-body${compact && !expanded ? " tweet-body-clamped" : ""}`}>{text}</p>
    {compact && (clipped || expanded) && <Button className="tweet-expand" size="small" theme="borderless" icon={expanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />} aria-expanded={expanded} aria-controls={id} onClick={() => setExpanded(!expanded)}>{t(expanded ? "archive.collapseText" : "archive.expandText")}</Button>}
  </div>;
}

export function TweetsPage() {
  useTranslation(); const client = useQueryClient();
  const [filters, setFilters] = useState(readFilters);
  const [search, setSearch] = useState(filters.get("search") ?? "");
  const [preview, setPreview] = useState<{ src: string; video: boolean; title: string } | null>(null);
  const debounced = useDebouncedValue(search, 300);
  useEffect(() => { function restore() { const next = readFilters(); setFilters(next); setSearch(next.get("search") ?? ""); } window.addEventListener("popstate", restore); return () => window.removeEventListener("popstate", restore); }, []);
  function change(patch: Record<string, string>, replace = false) {
    const next = readFilters(); for (const [key, value] of Object.entries(patch)) { if (value) next.set(key, value); else next.delete(key); }
    if (!("page" in patch)) next.delete("page");
    window.history[replace ? "replaceState" : "pushState"](null, "", `/tweets${next.size ? `?${next}` : ""}`); setFilters(next);
  }
  useEffect(() => { if (debounced !== (readFilters().get("search") ?? "")) change({ search: debounced }, true); }, [debounced]);
  const compact = filters.get("view") !== "comfortable";
  const retweets = ["exclude", "only"].includes(filters.get("retweets") ?? "") ? filters.get("retweets")! : "";
  const query = useMemo(() => { const q = new URLSearchParams(filters); q.delete("view"); q.set("pageSize", ["10", "20", "50", "100"].includes(filters.get("pageSize") ?? "") ? filters.get("pageSize")! : "20"); q.set("sort", filters.get("sort") === "asc" ? "asc" : "desc"); return q; }, [filters]);
  const tweets = useQuery({ queryKey: [...tweetsQueryKey, query.toString()], queryFn: ({ signal }) => getTweets(query, signal), placeholderData: previous => previous, refetchInterval: 15_000 });
  const retry = useMutation({ mutationFn: retryTweetMedia, onSuccess: () => { toast.success({ message: t("archive.retryQueued") }); void client.invalidateQueries({ queryKey: tweetsQueryKey }); }, onError: error => toast.error({ message: t(error.message, { defaultValue: error.message }) }) });
  const page = tweets.data;
  function openMedia(tweet: ArchivedTweet, record: ArchivedTweet["downloads"][number]) {
    const video = /\.(mp4|webm|mov|m4v|ogv)$/i.test(record.filePath);
    setPreview({ src: record.fileUrl ?? `/api/library/downloads/${record.id}/file`, video, title: `@${tweet.author.screenName} · ${tweet.id}` });
  }
  return <div className={`workspace-page tweet-archive${compact ? " tweet-archive-compact" : ""}`}>
    <div className="page-heading"><h1 className="page-title">{t("archive.title")}</h1><Button type="tertiary" icon={<RefreshCw size={16} />} loading={tweets.isFetching} disabled={tweets.isFetching} aria-busy={tweets.isFetching} onClick={() => void tweets.refetch()}>{t("刷新数据")}</Button></div>
    <div className="page-toolbar archive-filters">
      <div className="toolbar-field archive-search"><label htmlFor="archive-search">{t("archive.search")}</label><Input id="archive-search" prefix={<Search size={16} />} value={search} onChange={setSearch} showClear /></div>
      <div className="toolbar-field"><label htmlFor="archive-author">{t("archive.author")}</label><Input id="archive-author" value={filters.get("author") ?? ""} onChange={value => change({ author: value }, true)} placeholder={t("所有用户")} showClear /></div>
      <div className="toolbar-field archive-date">
        <label htmlFor="archive-date">{t("发布时间")}</label>
        <DatePicker type="dateRange" format="yyyy-MM-dd" onChangeWithDateFirst={false} dropdownClassName="archive-date-picker"
          value={filters.get("from") && filters.get("to") ? [filters.get("from")!, filters.get("to")!] : []}
          onChange={value => { const dates = Array.isArray(value) ? value : []; change({ from: String(dates[0] ?? ""), to: String(dates[1] ?? "") }); }}
          triggerRender={() => <Button id="archive-date" aria-label={t("发布时间")} icon={<CalendarDays size={16} />}>{filters.has("from") || filters.has("to") ? `${filters.get("from") || t("不限")} ~ ${filters.get("to") || t("不限")}` : t("全部时间")}</Button>}
          showClear />
      </div>
      <div className="toolbar-field"><label id="archive-sort-label" htmlFor="archive-sort">{t("排序顺序")}</label><Select id="archive-sort" aria-labelledby="archive-sort-label" value={filters.get("sort") === "asc" ? "asc" : "desc"} onChange={value => change({ sort: String(value) })} optionList={[{ value: "desc", label: t("最新发布优先"), showTick: false }, { value: "asc", label: t("最早发布优先"), showTick: false }]} /></div>
      <div className="toolbar-field"><label id="archive-media-label" htmlFor="archive-media">{t("archive.mediaType")}</label><Select id="archive-media" aria-labelledby="archive-media-label" value={filters.get("mediaType") ?? ""} onChange={value => change({ mediaType: String(value) })} optionList={[{ value: "", label: t("全部") }, { value: "text", label: t("archive.textOnly") }, { value: "photo", label: t("图片") }, { value: "video", label: t("视频") }, { value: "animated_gif", label: "GIF" }]} /></div>
    </div>
    <div className="archive-results-bar">
      <div className="archive-retweets" role="radiogroup" aria-label={t("archive.retweetFilter")}><RadioGroup name="archive-retweets" type="button" buttonSize="middle" value={retweets} onChange={event => change({ retweets: String(event.target.value) })} options={[{ value: "", label: t("archive.allTweets") }, { value: "exclude", label: t("archive.hideRetweets") }, { value: "only", label: t("archive.onlyRetweets") }]} /></div>
      {page && <span className="archive-result-count" aria-live="polite">{t("archive.total", { count: page.pagination.total })}</span>}
      <div className="archive-layout" role="radiogroup" aria-label={t("archive.layout")}><RadioGroup name="archive-layout" type="button" buttonSize="middle" value={compact ? "compact" : "comfortable"} onChange={event => change({ view: event.target.value === "comfortable" ? "comfortable" : "", page: filters.get("page") ?? "" })} options={[{ value: "compact", label: <span className="archive-layout-option"><AlignJustify size={14} />{t("archive.compact")}</span> }, { value: "comfortable", label: <span className="archive-layout-option"><Rows3 size={14} />{t("archive.comfortable")}</span> }]} /></div>
    </div>
    {(filters.has("id") || filters.has("jobId") || filters.has("author") || filters.has("from") || filters.has("to") || filters.has("mediaType") || filters.has("search") || retweets) && <div className="archive-active-filters">{(filters.has("id") || filters.has("jobId")) && <Tag>{filters.has("jobId") ? t("archive.taskFilter", { id: filters.get("jobId") }) : t("archive.tweetFilter", { id: filters.get("id") })}</Tag>}<Button size="small" theme="borderless" type="tertiary" onClick={() => { setSearch(""); change({ id: "", jobId: "", author: "", from: "", to: "", mediaType: "", search: "", retweets: "" }); }}>{t("archive.clearFilters")}</Button></div>}
    {tweets.isError && <Alert type="error" message={tweets.error.message} action={<Button onClick={() => void tweets.refetch()}>{t("重试")}</Button>} />}
    {!page && tweets.isLoading && <ListSkeleton />}
    {page?.items.length === 0 && <Empty description={t("archive.empty")} />}
    <div className="tweet-list">{page?.items.map(tweet => {
      const retweeted = tweet.related?.find(related => related.id === tweet.retweetedTweetId && related.text);
      return <article key={tweet.id} className="tweet-entry">
        <header className="tweet-header">
          <Avatar>{tweet.author.name?.slice(0, 1) || tweet.author.screenName?.slice(0, 1) || "X"}</Avatar>
          <div className="tweet-identity"><strong>{tweet.author.name || tweet.author.screenName}</strong><button type="button" className="tweet-author" onClick={() => change({ author: tweet.author.screenName })}>@{tweet.author.screenName}</button><time dateTime={tweet.createdAt}>{formatDateTime(tweet.createdAt)}</time></div>
          <Tooltip content={t("archive.original")}><a className="tweet-original" aria-label={t("archive.original")} href={tweet.url} target="_blank" rel="noreferrer"><ExternalLink size={16} /></a></Tooltip>
        </header>
        <div className={`tweet-content${tweet.downloads.length ? " tweet-has-media" : ""}`}>
          <div className="tweet-copy">
            {tweet.retweetedTweetId && <div className="tweet-repost"><Repeat2 size={13} /><span>{retweeted ? t("archive.repostedFrom", { author: retweeted.author.screenName }) : t("archive.retweet")}</span></div>}
            <TweetText text={retweeted?.text ?? tweet.text} compact={compact} />
            {tweet.related?.filter(related => related.id !== retweeted?.id).map(related => <blockquote className="tweet-related" key={related.id}><a href={`/tweets?id=${related.id}`}>@{related.author.screenName} · {related.id}</a><TweetText text={related.text} compact={compact} /></blockquote>)}
            {tweet.media.length > 0 && tweet.downloads.length === 0 && tweet.mediaStatus !== "not_requested" && <Tag tone="warning">{t("archive.mediaMissing")}</Tag>}
            <div className="tweet-relations">{([["replyToId", "archive.reply"], ["quotedTweetId", "archive.quote"], ["retweetedTweetId", "archive.retweet"]] as const).map(([key, label]) => tweet[key] ? <a key={key} href={`/tweets?id=${tweet[key]}`}>{t(label)} {tweet[key]}</a> : null)}</div>
            {tweet.media.length > 0 && !["completed", "not_requested"].includes(tweet.mediaStatus) && <Button className="tweet-retry" size="small" theme="borderless" type="tertiary" icon={<RefreshCw size={13} />} loading={retry.isPending && retry.variables === tweet.id} disabled={retry.isPending && retry.variables === tweet.id} aria-busy={retry.isPending && retry.variables === tweet.id} onClick={() => retry.mutate(tweet.id)}>{t("archive.retryMedia")}</Button>}
            {tweet.mediaError && <p className="text-xs text-danger break-words">{tweet.mediaError}</p>}
          </div>
          {tweet.downloads.length > 0 && <div className={`tweet-media${tweet.downloads.length > 1 ? " tweet-media-multiple" : ""}`}>{tweet.downloads.map(record => {
            const video = /\.(mp4|webm|mov|m4v|ogv)$/i.test(record.filePath); const src = record.fileUrl ?? `/api/library/downloads/${record.id}/file`;
            return <div key={record.id} className="tweet-media-item">{video ? <video aria-label={`${tweet.author.screenName} · ${tweet.id} · ${record.id}`} controls playsInline preload="metadata" src={src} poster={`/api/library/downloads/${record.id}/preview`} /> : <button type="button" aria-label={`${t("archive.openImage")} · @${tweet.author.screenName} · ${tweet.id} · ${record.id}`} onClick={() => openMedia(tweet, record)}><img src={src} loading="lazy" alt={`${tweet.author.screenName} · ${tweet.id}`} /></button>}</div>;
          })}</div>}
        </div>
      </article>;
    })}</div>
    {page && <Pagination page={page.pagination.page} pageSize={page.pagination.pageSize} total={page.pagination.total} onPageChange={value => change({ page: String(value) })} onPageSizeChange={value => change({ pageSize: String(value) })} />}
    <MediaModal open={preview !== null} onClose={() => setPreview(null)} title={preview?.title}>{preview && <img className="max-h-[75dvh] max-w-full mx-auto object-contain" src={preview.src} alt={preview.title} />}</MediaModal>
  </div>;
}
