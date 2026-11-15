import React, { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlignJustify, CalendarDays, ChevronDown, ChevronLeft, ChevronRight, ChevronUp, Copy, ExternalLink, FileText, Play, RefreshCw, Repeat2, Rows3, Search, SlidersHorizontal, X } from "lucide-react";
import { Button, DatePicker, Input, RadioGroup, Select } from "@douyinfe/semi-ui";
import { getTweetAuthors, getTweets, retryTweetMedia, tweetAuthorsQueryKey, tweetsQueryKey, type ArchivedTweet, type TweetPage } from "../lib/archive";
import { t, useTranslation } from "../lib/i18n";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import { copyToClipboard, formatDateTime } from "../components/common/CommonUI";
import { Alert, Avatar, Drawer, Empty, ListSkeleton, MediaModal, Pagination, Tag, Tooltip, toast } from "../components/semi";

function readFilters() { return new URLSearchParams(window.location.search); }

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// 旧归档数据可能缺少数组字段（后端返回 null）。页面里有大量 .length/.map，
// 一条坏数据就会让整个页面白屏，这里在渲染前统一补成空数组。
function normalizeArchivedTweet(tweet: ArchivedTweet): ArchivedTweet {
  return {
    ...tweet,
    media: Array.isArray(tweet.media) ? tweet.media : [],
    downloads: Array.isArray(tweet.downloads) ? tweet.downloads : [],
    jobIds: Array.isArray(tweet.jobIds) ? tweet.jobIds : [],
  };
}

function normalizeTweetPage(page: TweetPage | undefined): TweetPage | undefined {
  return page && { ...page, items: (Array.isArray(page.items) ? page.items : []).map(normalizeArchivedTweet) };
}

function TweetText({ text, compact, highlight = "" }: { text: string; compact: boolean; highlight?: string }) {
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
  const segments = highlight.trim() ? text.split(new RegExp(`(${escapeRegExp(highlight.trim())})`, "ig")) : [text];
  return <div className="tweet-text">
    <p id={id} ref={body} className={`tweet-body${compact && !expanded ? " tweet-body-clamped" : ""}`}>{segments.map((segment, index) => highlight.trim() && index % 2 === 1 ? <mark key={index}>{segment}</mark> : segment)}</p>
    {compact && (clipped || expanded) && <Button className="tweet-expand" size="small" theme="borderless" icon={expanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />} aria-expanded={expanded} aria-controls={id} onClick={() => setExpanded(!expanded)}>{t(expanded ? "archive.collapseText" : "archive.expandText")}</Button>}
  </div>;
}

export function TweetsPage() {
  useTranslation(); const client = useQueryClient();
  const [filters, setFilters] = useState(readFilters);
  const [search, setSearch] = useState(filters.get("search") ?? "");
  const [author, setAuthor] = useState(filters.get("author") ?? "");
  const debouncedAuthor = useDebouncedValue(author, 300);
  const [detailId, setDetailId] = useState<string | null>(null);
  const [detailSnapshot, setDetailSnapshot] = useState<ArchivedTweet | null>(null);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [snapshot, setSnapshot] = useState<{ key: string; page: TweetPage } | null>(null);
  const [preview, setPreview] = useState<{ tweet: ArchivedTweet; index: number } | null>(null);
  const debounced = useDebouncedValue(search, 300);
  useEffect(() => { function restore() { const next = readFilters(); setFilters(next); setSearch(next.get("search") ?? ""); setAuthor(next.get("author") ?? ""); } window.addEventListener("popstate", restore); return () => window.removeEventListener("popstate", restore); }, []);
  function change(patch: Record<string, string>, replace = false) {
    const next = readFilters(); for (const [key, value] of Object.entries(patch)) { if (value) next.set(key, value); else next.delete(key); }
    if (!("page" in patch)) next.delete("page");
    window.history[replace ? "replaceState" : "pushState"](null, "", `/tweets${next.size ? `?${next}` : ""}`); setFilters(next);
  }
  useEffect(() => { if (debounced !== (readFilters().get("search") ?? "")) change({ search: debounced }, true); }, [debounced]);
  useEffect(() => { if (debouncedAuthor !== (readFilters().get("author") ?? "")) change({ author: debouncedAuthor }, true); }, [debouncedAuthor]);
  const compact = filters.get("view") !== "comfortable";
  const retweets = ["exclude", "only"].includes(filters.get("retweets") ?? "") ? filters.get("retweets")! : "";
  const query = useMemo(() => { const q = new URLSearchParams(filters); q.delete("view"); q.set("pageSize", ["10", "20", "50", "100"].includes(filters.get("pageSize") ?? "") ? filters.get("pageSize")! : "20"); q.set("sort", filters.get("sort") === "asc" ? "asc" : "desc"); return q; }, [filters]);
  const tweets = useQuery({ queryKey: [...tweetsQueryKey, query.toString()], queryFn: ({ signal }) => getTweets(query, signal), placeholderData: previous => previous, refetchInterval: 15_000 });
  const authors = useQuery({ queryKey: tweetAuthorsQueryKey, queryFn: ({ signal }) => getTweetAuthors(signal), staleTime: 5 * 60_000 });
  const retry = useMutation({ mutationFn: retryTweetMedia, onSuccess: () => { toast.success({ message: t("archive.retryQueued") }); void client.invalidateQueries({ queryKey: tweetsQueryKey }); }, onError: error => toast.error({ message: t(error.message, { defaultValue: error.message }) }) });
  const queryIdentity = query.toString();
  useEffect(() => {
    if (!tweets.data || tweets.isPlaceholderData) return;
    const incoming = tweets.data;
    setSnapshot(current => {
      if (current?.key === queryIdentity && incoming.items.some(item => !current.page.items.some(previous => previous.id === item.id))) return current;
      return { key: queryIdentity, page: incoming };
    });
  }, [tweets.data, tweets.isPlaceholderData, queryIdentity]);
  const page = normalizeTweetPage(snapshot?.key === queryIdentity ? snapshot.page : tweets.data);
  const authorOptions = useMemo(() => {
    const options = authors.data ?? [];
    if (author && !options.some(option => option.value.toLowerCase() === author.toLowerCase())) {
      return [{ value: author, label: `@${author.replace(/^@/, "")}` }, ...options];
    }
    return options;
  }, [author, authors.data]);
  const newRecords = !tweets.isPlaceholderData && snapshot?.key === queryIdentity ? (tweets.data?.items ?? []).filter(item => !snapshot.page.items.some(previous => previous.id === item.id)).length : 0;
  const detail = detailId ? page?.items.find(tweet => tweet.id === detailId) ?? detailSnapshot : null;
  const detailReposted = detail?.related?.find(related => related.id === detail.retweetedTweetId && related.text);
  function openMedia(tweet: ArchivedTweet, record: ArchivedTweet["downloads"][number]) {
    setPreview({ tweet, index: Math.max(0, tweet.downloads.findIndex(item => item.id === record.id)) });
  }
  const previewRecord = preview?.tweet.downloads[preview.index];
  const previewVideo = previewRecord ? /\.(mp4|webm|mov|m4v|ogv)$/i.test(previewRecord.filePath) : false;
  return <div className={`workspace-page tweet-archive${compact ? " tweet-archive-compact" : ""}`}>
    <div className="page-heading"><h1 className="page-title">{t("archive.title")}</h1><Button type="tertiary" icon={<RefreshCw size={16} />} loading={tweets.isFetching} disabled={tweets.isFetching} aria-busy={tweets.isFetching} onClick={() => void tweets.refetch().then(result => { if (result.data) setSnapshot({ key: queryIdentity, page: result.data }); })}>{t("刷新数据")}</Button></div>
    <div className={`page-toolbar archive-filters${filtersOpen ? " archive-filters-expanded" : ""}`}>
      <div className="toolbar-field archive-search"><label htmlFor="archive-search">{t("archive.search")}</label><Input id="archive-search" prefix={<Search size={16} />} value={search} onChange={setSearch} showClear /></div>
      <Button className="archive-filter-toggle" icon={<SlidersHorizontal size={16} />} aria-expanded={filtersOpen} onClick={() => setFiltersOpen(!filtersOpen)}>{t("筛选")}</Button>
      <div className="toolbar-field"><label htmlFor="archive-author">{t("archive.author")}</label><Input id="archive-author" list="archive-author-options" value={author} onChange={setAuthor} placeholder={t("所有用户")} showClear /><datalist id="archive-author-options">{authorOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</datalist></div>
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
    {(filters.has("id") || filters.has("jobId") || filters.has("author") || filters.has("from") || filters.has("to") || filters.has("mediaType") || filters.has("search") || retweets) && <div className="archive-active-filters active-filter-tags">{["id", "jobId", "author", "from", "to", "mediaType", "search", "retweets"].filter(key => filters.get(key)).map(key => <button type="button" className="filter-chip" key={key} aria-label={`${t("移除筛选")} ${filters.get(key)}`} onClick={() => { if (key === "search") setSearch(""); if (key === "author") setAuthor(""); change({ [key]: "" }); }}>{t(({ id: "推文 ID", jobId: "来源任务", author: "archive.author", from: "开始日期", to: "结束日期", mediaType: "archive.mediaType", search: "archive.search", retweets: "archive.retweetFilter" } as Record<string, string>)[key])}: {filters.get(key)}<X size={12} /></button>)}<Button size="small" theme="borderless" type="tertiary" onClick={() => { setSearch(""); setAuthor(""); change({ id: "", jobId: "", author: "", from: "", to: "", mediaType: "", search: "", retweets: "" }); }}>{t("archive.clearFilters")}</Button></div>}
    {(tweets.isPlaceholderData || search !== debounced || author !== debouncedAuthor) && <div role="status" className="archive-query-status">{t("正在应用筛选...")}</div>}
    {newRecords > 0 && <Button className="archive-new-records" icon={<RefreshCw size={14} />} onClick={() => { if (tweets.data) setSnapshot({ key: queryIdentity, page: tweets.data }); }}>{t("有 {{count}} 条新记录", { count: newRecords })}</Button>}
    {tweets.isError && <Alert type="error" message={tweets.error.message} action={<Button onClick={() => void tweets.refetch()}>{t("重试")}</Button>} />}
    {!page && tweets.isLoading && <ListSkeleton />}
    {page?.items.length === 0 && <Empty description={filters.size ? t("没有匹配的推文") : t("archive.empty")} />}
    <div className="tweet-list">{page?.items.map(tweet => {
      const retweeted = tweet.related?.find(related => related.id === tweet.retweetedTweetId && related.text);
      return <article key={tweet.id} className="tweet-entry">
        <header className="tweet-header">
          <Avatar>{tweet.author.name?.slice(0, 1) || tweet.author.screenName?.slice(0, 1) || "X"}</Avatar>
          <div className="tweet-identity"><strong>{tweet.author.name || tweet.author.screenName}</strong><button type="button" className="tweet-author" title={t("筛选此作者")} onClick={() => { setAuthor(tweet.author.screenName); change({ author: tweet.author.screenName }); }}>@{tweet.author.screenName}</button><time dateTime={tweet.createdAt}>{formatDateTime(tweet.createdAt)}</time></div>
          <Tooltip content={t("复制正文")}><Button size="small" theme="borderless" icon={<Copy size={15} />} aria-label={t("复制正文")} onClick={() => void copyToClipboard(tweet.text, t("正文"))} /></Tooltip>
          <Tooltip content={t("查看归档详情")}><Button size="small" theme="borderless" icon={<FileText size={15} />} aria-label={t("查看归档详情")} onClick={() => { setDetailSnapshot(tweet); setDetailId(tweet.id); }} /></Tooltip>
          <Tooltip content={t("archive.original")}><a className="tweet-original" aria-label={t("archive.original")} href={tweet.url} target="_blank" rel="noreferrer"><ExternalLink size={16} /></a></Tooltip>
        </header>
        <div className={`tweet-content${tweet.downloads.length ? " tweet-has-media" : ""}`}>
          <div className="tweet-copy">
            {tweet.retweetedTweetId && <div className="tweet-repost"><Repeat2 size={13} /><span>{retweeted ? t("archive.repostedFrom", { author: retweeted.author.screenName }) : t("archive.retweet")}</span></div>}
            <TweetText text={retweeted?.text ?? tweet.text} compact={compact} highlight={debounced} />
            {tweet.related?.filter(related => related.id !== retweeted?.id).map(related => <blockquote className="tweet-related" key={related.id}><a href={`/tweets?id=${related.id}`}>@{related.author.screenName} · {related.id}</a><TweetText text={related.text} compact={compact} highlight={debounced} /></blockquote>)}
            {tweet.media.length > 0 && tweet.downloads.length === 0 && tweet.mediaStatus !== "not_requested" && <Tag tone="warning">{t("archive.mediaMissing")}</Tag>}
            <div className="tweet-relations">{([["replyToId", "archive.reply"], ["quotedTweetId", "archive.quote"], ["retweetedTweetId", "archive.retweet"]] as const).map(([key, label]) => tweet[key] ? <a key={key} href={`/tweets?id=${tweet[key]}`}>{t(label)} {tweet[key]}</a> : null)}</div>
            {tweet.media.length > 0 && !["completed", "not_requested"].includes(tweet.mediaStatus) && <Button className="tweet-retry" size="small" theme="borderless" type="tertiary" icon={<RefreshCw size={13} />} loading={retry.isPending && retry.variables === tweet.id} disabled={retry.isPending && retry.variables === tweet.id} aria-busy={retry.isPending && retry.variables === tweet.id} onClick={() => retry.mutate(tweet.id)}>{t("archive.retryMedia")}</Button>}
            {tweet.mediaError && <p className="text-xs text-danger break-words">{tweet.mediaError}</p>}
            <div className="tweet-archive-meta"><time dateTime={tweet.archivedAt}>{t("归档于")} {formatDateTime(tweet.archivedAt)}</time><span>{tweet.downloads.length} {t("个文件")}</span>{tweet.jobIds.length > 0 && <a href={`/tasks?q=${tweet.jobIds[0]}`}>{t("来源任务")} #{tweet.jobIds[0]}</a>}{tweet.mediaStatus !== "completed" && tweet.mediaStatus !== "not_requested" && tweet.media.length > 0 && <span className="text-warning-strong">{tweet.downloads.length ? t("媒体部分缺失") : t("媒体待处理")}</span>}</div>
          </div>
          {tweet.downloads.length > 0 && <div className={`tweet-media${tweet.downloads.length > 1 ? " tweet-media-multiple" : ""}`}>{(compact ? tweet.downloads.slice(0, 2) : tweet.downloads).map(record => {
            const video = /\.(mp4|webm|mov|m4v|ogv)$/i.test(record.filePath); const src = record.fileUrl ?? `/api/library/downloads/${record.id}/file`;
            return <div key={record.id} className="tweet-media-item">{video && !compact ? <video aria-label={`${tweet.author.screenName} · ${tweet.id} · ${record.id}`} controls playsInline preload="metadata" src={src} poster={`/api/library/downloads/${record.id}/preview`} /> : <button type="button" aria-label={`${video ? t("播放视频") : t("archive.openImage")} · @${tweet.author.screenName} · ${tweet.id} · ${record.id}`} onClick={() => openMedia(tweet, record)}><img src={video ? `/api/library/downloads/${record.id}/preview` : src} loading="lazy" alt={`${tweet.author.screenName} · ${tweet.id}`} />{video && <span className="tweet-play-icon"><Play size={20} fill="currentColor" /></span>}</button>}</div>;
          })}{compact && tweet.downloads.length > 2 && <button className="tweet-more-media" onClick={() => { setDetailSnapshot(tweet); setDetailId(tweet.id); }}>+{tweet.downloads.length - 2}</button>}</div>}
        </div>
      </article>;
    })}</div>
    {page && <Pagination page={page.pagination.page} pageSize={page.pagination.pageSize} total={page.pagination.total} onPageChange={value => change({ page: String(value) })} onPageSizeChange={value => change({ pageSize: String(value) })} />}
    <Drawer open={Boolean(detail)} onClose={() => setDetailId(null)} title={detail ? `@${detail.author.screenName}` : t("归档详情")} width="44rem">{detail && <div className="space-y-4"><div className="text-xs text-fg-muted">{t("推文 ID")}: {detail.id} · {formatDateTime(detail.createdAt)}</div><TweetText text={detailReposted?.text ?? detail.text} compact={false} highlight={debounced} />{detail.related?.filter(related => related.id !== detailReposted?.id).map(related => <blockquote className="tweet-related" key={related.id}><a href={`/tweets?id=${related.id}`}>@{related.author.screenName}</a><TweetText text={related.text} compact={false} highlight={debounced} /></blockquote>)}<div className="tweet-archive-meta">{t("归档于")} {formatDateTime(detail.archivedAt)}<span>{t("来源任务")}: {detail.jobIds.map(id => `#${id}`).join(", ") || "—"}</span></div>{detail.downloads.map(record => <button className="tweet-detail-file" key={record.id} onClick={() => openMedia(detail, record)}><FileText size={16} /><span>{record.filePath.split(/[\\/]/).pop()}</span><ExternalLink size={14} /></button>)}<div className="flex gap-2"><Button icon={<Copy size={16} />} onClick={() => void copyToClipboard(detail.url, t("推文链接"))}>{t("复制链接")}</Button><a href={detail.url} target="_blank" rel="noreferrer" className="text-sm text-brand-600">{t("archive.original")}</a>{detail.downloads[0] && <a href={`/gallery?q=${encodeURIComponent(detail.downloads[0].filePath.split(/[\\/]/).pop() || "")}`} className="text-sm text-brand-600">{t("定位媒体库")}</a>}</div></div>}</Drawer>
    <MediaModal open={preview !== null} onClose={() => setPreview(null)} title={preview ? `@${preview.tweet.author.screenName} · ${preview.tweet.id}` : ""}>{preview && previewRecord && <div className="space-y-3"><div className="relative flex min-h-[18rem] items-center justify-center bg-black/95 p-2"><Button theme="borderless" type="tertiary" icon={<ChevronLeft size={20} />} aria-label={t("上一个媒体")} disabled={preview.index <= 0} onClick={() => setPreview(current => current && current.index > 0 ? { ...current, index: current.index - 1 } : current)} className="absolute left-2 z-10 text-white" />{previewVideo ? <video className="max-h-[75dvh] max-w-full mx-auto" src={previewRecord.fileUrl ?? `/api/library/downloads/${previewRecord.id}/file`} poster={`/api/library/downloads/${previewRecord.id}/preview`} controls autoPlay playsInline /> : <img className="max-h-[75dvh] max-w-full mx-auto object-contain" src={previewRecord.fileUrl ?? `/api/library/downloads/${previewRecord.id}/file`} alt={`@${preview.tweet.author.screenName} · ${preview.tweet.id}`} />}<Button theme="borderless" type="tertiary" icon={<ChevronRight size={20} />} aria-label={t("下一个媒体")} disabled={preview.index >= preview.tweet.downloads.length - 1} onClick={() => setPreview(current => current && current.index < current.tweet.downloads.length - 1 ? { ...current, index: current.index + 1 } : current)} className="absolute right-2 z-10 text-white" /></div><div className="flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted"><span>{preview.index + 1} / {preview.tweet.downloads.length}</span><a className="text-brand-600" href={`/gallery?q=${encodeURIComponent(previewRecord.filePath.split(/[\\/]/).pop() || "")}`}>{t("定位媒体库")}</a></div></div>}</MediaModal>
  </div>;
}
