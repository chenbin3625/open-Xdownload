import React, { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarDays, ExternalLink, RefreshCw, Search } from "lucide-react";
import { Button, DatePicker, Input, Select, Typography } from "@douyinfe/semi-ui";
import { getTweets, retryTweetMedia, tweetsQueryKey, type ArchivedTweet } from "../lib/archive";
import { t, useTranslation } from "../lib/i18n";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import { formatDateTime } from "../components/common/CommonUI";
import { Alert, Avatar, Empty, ListSkeleton, MediaModal, Pagination, Tag, toast } from "../components/semi";

function readFilters() { return new URLSearchParams(window.location.search); }

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
  const query = useMemo(() => { const q = new URLSearchParams(filters); q.set("pageSize", "20"); q.set("sort", filters.get("sort") === "asc" ? "asc" : "desc"); return q; }, [filters]);
  const tweets = useQuery({ queryKey: [...tweetsQueryKey, query.toString()], queryFn: ({ signal }) => getTweets(query, signal), placeholderData: previous => previous, refetchInterval: 15_000 });
  const retry = useMutation({ mutationFn: retryTweetMedia, onSuccess: () => { toast.success({ message: t("archive.retryQueued") }); void client.invalidateQueries({ queryKey: tweetsQueryKey }); }, onError: error => toast.error({ message: t(error.message, { defaultValue: error.message }) }) });
  const page = tweets.data;
  function openMedia(tweet: ArchivedTweet, record: ArchivedTweet["downloads"][number]) {
    const video = /\.(mp4|webm|mov|m4v|ogv)$/i.test(record.filePath);
    setPreview({ src: record.fileUrl ?? `/api/library/downloads/${record.id}/file`, video, title: `@${tweet.author.screenName} · ${tweet.id}` });
  }
  return <div className="workspace-page tweet-archive">
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
    {(filters.has("id") || filters.has("jobId") || filters.has("author") || filters.has("from") || filters.has("to") || filters.has("mediaType") || filters.has("search")) && <div className="flex flex-wrap items-center gap-2">{(filters.has("id") || filters.has("jobId")) && <Tag>{filters.has("jobId") ? t("archive.taskFilter", { id: filters.get("jobId") }) : t("archive.tweetFilter", { id: filters.get("id") })}</Tag>}<Button theme="borderless" type="tertiary" onClick={() => { setSearch(""); change({ id: "", jobId: "", author: "", from: "", to: "", mediaType: "", search: "" }); }}>{t("archive.clearFilters")}</Button></div>}
    {tweets.isError && <Alert type="error" message={tweets.error.message} action={<Button onClick={() => void tweets.refetch()}>{t("重试")}</Button>} />}
    {!page && tweets.isLoading && <ListSkeleton />}
    {page?.items.length === 0 && <Empty description={t("archive.empty")} />}
    <div className="tweet-list">{page?.items.map(tweet => <article key={tweet.id} className="tweet-entry">
      <header className="flex items-start gap-3"><Avatar>{tweet.author.name?.slice(0, 1) ?? "X"}</Avatar><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-x-2 gap-y-1"><strong className="text-sm break-words">{tweet.author.name}</strong><Button theme="borderless" type="tertiary" onClick={() => change({ author: tweet.author.screenName })}>@{tweet.author.screenName}</Button></div><time className="text-xs text-fg-subtle" dateTime={tweet.createdAt}>{formatDateTime(tweet.createdAt)}</time></div><Typography.Text link={{ href: tweet.url, target: "_blank", rel: "noreferrer" }}><span className="inline-flex items-center gap-1"><ExternalLink size={16} />{t("archive.original")}</span></Typography.Text></header>
      <p className="tweet-body">{tweet.text}</p>
      <div className="tweet-media">{tweet.downloads.map(record => {
        const video = /\.(mp4|webm|mov|m4v|ogv)$/i.test(record.filePath); const src = record.fileUrl ?? `/api/library/downloads/${record.id}/file`;
        return <div key={record.id} className="tweet-media-item">{video ? <video aria-label={`${tweet.author.screenName} · ${tweet.id} · ${record.id}`} controls playsInline preload="metadata" src={src} poster={`/api/library/downloads/${record.id}/preview`} /> : <button aria-label={`${t("archive.openImage")} · @${tweet.author.screenName} · ${tweet.id} · ${record.id}`} onClick={() => openMedia(tweet, record)}><img src={src} loading="lazy" alt={`${tweet.author.screenName} · ${tweet.id}`} /></button>}</div>;
      })}</div>
      {tweet.media.length > 0 && tweet.downloads.length === 0 && tweet.mediaStatus !== "not_requested" && <Tag tone="warning">{t("archive.mediaMissing")}</Tag>}
      {tweet.related?.map(related => <blockquote className="tweet-related" key={related.id}><a href={`/tweets?id=${related.id}`}>@{related.author.screenName} · {related.id}</a><p>{related.text}</p></blockquote>)}
      <div className="flex flex-wrap gap-3 text-xs">{([["replyToId", "archive.reply"], ["quotedTweetId", "archive.quote"], ["retweetedTweetId", "archive.retweet"]] as const).map(([key, label]) => tweet[key] ? <a className="text-brand-600" key={key} href={`/tweets?id=${tweet[key]}`}>{t(label)} {tweet[key]}</a> : null)}</div>
      {tweet.media.length > 0 && !["completed", "not_requested"].includes(tweet.mediaStatus) && <Button theme="borderless" type="tertiary" loading={retry.isPending && retry.variables === tweet.id} disabled={retry.isPending && retry.variables === tweet.id} aria-busy={retry.isPending && retry.variables === tweet.id} onClick={() => retry.mutate(tweet.id)}>{t("archive.retryMedia")}</Button>}
      {tweet.mediaError && <p className="text-xs text-danger break-words">{tweet.mediaError}</p>}
    </article>)}</div>
    {page && <Pagination page={page.pagination.page} pageSize={page.pagination.pageSize} total={page.pagination.total} onPageChange={value => change({ page: String(value) })} />}
    <MediaModal open={preview !== null} onClose={() => setPreview(null)} title={preview?.title}>{preview && <img className="max-h-[75dvh] max-w-full mx-auto object-contain" src={preview.src} alt={preview.title} />}</MediaModal>
  </div>;
}
