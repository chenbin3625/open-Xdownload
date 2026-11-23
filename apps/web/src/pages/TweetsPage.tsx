import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlignJustify, CalendarDays, ChevronLeft, ChevronRight, Copy, ExternalLink, FileText, RefreshCw, Rows3, Search, SlidersHorizontal, X } from "lucide-react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import RadioGroup from "@douyinfe/semi-ui/lib/es/radio/radioGroup";
import Select from "@douyinfe/semi-ui/lib/es/select";
import type { TweetData } from "../lib/api";
import { getTweetAuthors, getTweets, retryTweetMedia, tweetAuthorsQueryKey, tweetsQueryKey, type ArchivedTweet, type TweetPage } from "../lib/archive";
import { t, useTranslation } from "../lib/i18n";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import { copyToClipboard, formatDateTime } from "../components/common/CommonUI";
import { Alert, Empty, ListSkeleton } from "../components/semi/feedback";
import { Drawer, MediaModal } from "../components/semi/overlays";
import { Pagination } from "../components/semi/data";
import { toast } from "../components/semi/toast";
import { TweetCard, TweetText } from "../components/tweets/TweetCard";

const ArchiveDatePicker = lazy(() => import("../components/tweets/ArchiveDatePicker"));

function readFilters() { return new URLSearchParams(window.location.search); }

function sanitizeDateParam(value: string | null) {
  if (!value || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return "";
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isFinite(date.getTime()) && date.toISOString().slice(0, 10) === value ? value : "";
}

function isKeyboardOwnedTarget(target: EventTarget | null) {
  if (!(target instanceof Element)) return false;
  if (target instanceof HTMLMediaElement) return true;
  if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) {
    return true;
  }
  const editable = target.closest("[contenteditable]");
  return editable !== null && editable.getAttribute("contenteditable") !== "false";
}

const MAX_RELATED_NORMALIZE_DEPTH = 4;

// 旧归档数据（包括嵌套的 related 引用/转推）可能缺少数组字段（后端返回 null）。
// 页面里有大量 .length/.map，一条坏数据就会让整个页面白屏，这里递归补成空数组并防环。
function normalizeTweetData(tweet: TweetData, depth = 0, seen = new Set<TweetData>()): TweetData {
  if (seen.has(tweet) || depth > MAX_RELATED_NORMALIZE_DEPTH) {
    return {
      ...tweet,
      media: Array.isArray(tweet.media) ? tweet.media : [],
      related: undefined,
    };
  }
  seen.add(tweet);
  const related = Array.isArray(tweet.related)
    ? tweet.related
        .filter((item): item is TweetData => Boolean(item && typeof item === "object"))
        .map(item => normalizeTweetData(item, depth + 1, seen))
    : undefined;
  seen.delete(tweet);
  return {
    ...tweet,
    media: Array.isArray(tweet.media) ? tweet.media : [],
    related,
  };
}

function normalizeArchivedTweet(tweet: ArchivedTweet): ArchivedTweet {
  const normalized = normalizeTweetData(tweet);
  return {
    ...tweet,
    ...normalized,
    downloads: Array.isArray(tweet.downloads) ? tweet.downloads : [],
    jobIds: Array.isArray(tweet.jobIds) ? tweet.jobIds : [],
  };
}

function normalizeTweetPage(page: TweetPage | undefined): TweetPage | undefined {
  return page && { ...page, items: (Array.isArray(page.items) ? page.items : []).map(normalizeArchivedTweet) };
}

export function TweetsPage({ realtimeConnected = true }: { realtimeConnected?: boolean }) {
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
  const change = useCallback((patch: Record<string, string>, replace = false) => {
    const next = readFilters(); for (const [key, value] of Object.entries(patch)) { if (value) next.set(key, value); else next.delete(key); }
    if (!("page" in patch)) next.delete("page");
    const qs = next.toString();
    window.history[replace ? "replaceState" : "pushState"](null, "", `/tweets${qs ? `?${qs}` : ""}`); setFilters(next);
  }, []);
  const clearAllFilters = useCallback(() => {
    setSearch("");
    setAuthor("");
    change({ id: "", jobId: "", author: "", from: "", to: "", mediaType: "", search: "", retweets: "" });
  }, [change]);
  useEffect(() => { if (debounced !== (readFilters().get("search") ?? "")) change({ search: debounced }, true); }, [debounced]);
  useEffect(() => { if (debouncedAuthor !== (readFilters().get("author") ?? "")) change({ author: debouncedAuthor }, true); }, [debouncedAuthor]);
  useEffect(() => {
    if (!preview) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (isKeyboardOwnedTarget(event.target)) return;
      if (event.key === "ArrowLeft") {
        event.preventDefault();
        setPreview(current => current && current.index > 0 ? { ...current, index: current.index - 1 } : current);
      } else if (event.key === "ArrowRight") {
        event.preventDefault();
        setPreview(current => current && current.index < current.tweet.downloads.length - 1 ? { ...current, index: current.index + 1 } : current);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [preview !== null]);
  const compact = filters.get("view") !== "comfortable";
  const retweets = ["exclude", "only"].includes(filters.get("retweets") ?? "") ? filters.get("retweets")! : "";
  const fromDate = sanitizeDateParam(filters.get("from"));
  const toDate = sanitizeDateParam(filters.get("to"));
  const requestedPage = Math.max(1, Number.parseInt(filters.get("page") ?? "1", 10) || 1);
  const hasActiveFilters = Boolean(
    filters.get("id") ||
    filters.get("jobId") ||
    filters.get("author") ||
    fromDate ||
    toDate ||
    filters.get("mediaType") ||
    filters.get("search") ||
    retweets
  );
  const query = useMemo(() => {
    const q = new URLSearchParams(filters);
    q.delete("view");
    if (retweets) q.set("retweets", retweets); else q.delete("retweets");
    if (fromDate) q.set("from", fromDate); else q.delete("from");
    if (toDate) q.set("to", toDate); else q.delete("to");
    q.set("pageSize", ["10", "20", "50", "100"].includes(filters.get("pageSize") ?? "") ? filters.get("pageSize")! : "20");
    q.set("sort", filters.get("sort") === "asc" ? "asc" : "desc");
    return q;
  }, [filters, retweets, fromDate, toDate]);
  // SSE 连接正常时归档事件会推送刷新；轮询降级为低频兜底，断线时再缩短到 30s。
  const tweets = useQuery({ queryKey: [...tweetsQueryKey, query.toString()], queryFn: ({ signal }) => getTweets(query, signal), placeholderData: previous => previous, refetchInterval: realtimeConnected ? 60_000 : 30_000 });
  const authors = useQuery({ queryKey: tweetAuthorsQueryKey, queryFn: ({ signal }) => getTweetAuthors(signal), staleTime: 5 * 60_000 });
  const retry = useMutation({ mutationFn: retryTweetMedia, onSuccess: () => { toast.success({ message: t("archive.retryQueued") }); void client.invalidateQueries({ queryKey: tweetsQueryKey }); }, onError: error => toast.error({ message: t(error.message, { defaultValue: error.message }) }) });
  const queryIdentity = query.toString();
  const queryIdentityRef = useRef(queryIdentity);
  queryIdentityRef.current = queryIdentity;
  useEffect(() => {
    if (!tweets.data || tweets.isPlaceholderData) return;
    const incoming = tweets.data;
    setSnapshot(current => {
      if (current?.key === queryIdentity && current.page.items.length > 0 && incoming.items.length > 0) {
        const currentIds = new Set(current.page.items.map(item => item.id));
        const addedCount = incoming.items.filter(item => !currentIds.has(item.id)).length;
        // 仅当有新增记录且总数上升时才暂存当前视口以展示"有 N 条新记录"横幅，
        // 且仅保留最新响应中仍然存在的条目，绝不滞留已删除推文。
        if (addedCount > 0 && incoming.pagination.total > current.page.pagination.total) {
          const incomingByID = new Map(incoming.items.map(item => [item.id, item]));
          const merged: ArchivedTweet[] = [];
          for (const item of current.page.items) {
            const fresh = incomingByID.get(item.id);
            if (fresh) merged.push(fresh);
          }
          if (merged.length > 0) {
            return { key: queryIdentity, page: { ...incoming, items: merged } };
          }
        }
      }
      return { key: queryIdentity, page: incoming };
    });
  }, [tweets.data, tweets.isPlaceholderData, queryIdentity]);
  // 总页数缩小时收敛页码，避免删除/筛选后停在空页。
  useEffect(() => {
    if (!tweets.data || tweets.isPlaceholderData) return;
    if (search !== debounced || author !== debouncedAuthor) return;
    const totalPages = Math.max(
      1,
      tweets.data.pagination.totalPages ||
        (tweets.data.pagination.pageSize > 0 ? Math.ceil(tweets.data.pagination.total / tweets.data.pagination.pageSize) : 1),
    );
    if (requestedPage > totalPages) {
      change({ page: totalPages > 1 ? String(totalPages) : "" }, true);
    }
  }, [tweets.data, tweets.isPlaceholderData, search, debounced, author, debouncedAuthor, requestedPage, change]);
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
  const detailReposted = detail?.related?.find(related => related.id === detail.retweetedTweetId && Boolean(related.text || related.media?.length || related.author?.screenName));

  // 卡片用稳定回调 + id 传参，配合 TweetCard 的 React.memo：未变化的卡片不会重渲染。
  const pageItemsRef = useRef<ArchivedTweet[]>([]);
  pageItemsRef.current = page?.items ?? [];
  const openTweetMedia = useCallback((tweetId: string, record: ArchivedTweet["downloads"][number]) => {
    const tweet = pageItemsRef.current.find(item => item.id === tweetId);
    if (!tweet) return;
    setPreview({ tweet, index: Math.max(0, tweet.downloads.findIndex(item => item.id === record.id)) });
  }, []);
  const openTweetDetail = useCallback((tweetId: string) => {
    const tweet = pageItemsRef.current.find(item => item.id === tweetId);
    if (tweet) setDetailSnapshot(tweet);
    setDetailId(tweetId);
  }, []);
  const handleFilterAuthor = useCallback((screenName: string) => {
    setAuthor(screenName);
    change({ author: screenName });
  }, [change]);
  const handleRetryTweet = useCallback((id: string) => retry.mutate(id), [retry.mutate]);
  const previewRecord = preview?.tweet.downloads[preview.index];
  const previewVideo = previewRecord ? /\.(mp4|webm|mov|m4v|ogv)$/i.test(previewRecord.filePath) : false;
  return <div className={`workspace-page tweet-archive${compact ? " tweet-archive-compact" : ""}`}>
    <div className="page-heading">
      <h1 className="page-title">{t("archive.title")}</h1>
      <div className="page-actions">
        <div className="archive-layout" role="radiogroup" aria-label={t("archive.layout")}><RadioGroup name="archive-layout" type="button" buttonSize="middle" value={compact ? "compact" : "comfortable"} onChange={event => change({ view: event.target.value === "comfortable" ? "comfortable" : "", page: filters.get("page") ?? "" })} options={[{ value: "compact", label: <span className="archive-layout-option"><AlignJustify size={14} />{t("archive.compact")}</span> }, { value: "comfortable", label: <span className="archive-layout-option"><Rows3 size={14} />{t("archive.comfortable")}</span> }]} /></div>
        <Button type="tertiary" icon={<RefreshCw size={16} />} loading={tweets.isFetching} disabled={tweets.isFetching} aria-busy={tweets.isFetching} onClick={() => { const requestIdentity = queryIdentity; void tweets.refetch().then(result => { if (result.data && queryIdentityRef.current === requestIdentity) setSnapshot({ key: requestIdentity, page: result.data }); }); }}>{t("刷新数据")}</Button>
      </div>
    </div>
    <div className={`page-toolbar archive-filters${filtersOpen ? " archive-filters-expanded" : ""}`}>
      <div className="toolbar-status archive-retweets" role="radiogroup" aria-label={t("archive.retweetFilter")}><RadioGroup name="archive-retweets" type="button" buttonSize="middle" value={retweets} onChange={event => change({ retweets: String(event.target.value) })} options={[{ value: "", label: t("archive.allTweets") }, { value: "exclude", label: t("archive.hideRetweets") }, { value: "only", label: t("archive.onlyRetweets") }]} /></div>
      <div className="toolbar-filters">
      <div className="toolbar-search archive-search"><label className="sr-only" htmlFor="archive-search">{t("archive.search")}</label><Input id="archive-search" prefix={<Search size={16} />} placeholder={t("archive.search")} value={search} onChange={setSearch} showClear /></div>
      <Button className="archive-filter-toggle" type="tertiary" icon={<SlidersHorizontal size={16} />} aria-controls="archive-filter-fields" aria-expanded={filtersOpen} onClick={() => setFiltersOpen(!filtersOpen)}>{t("筛选")}</Button>
      <div className="archive-filter-fields" id="archive-filter-fields">
      <div className="toolbar-select"><label className="sr-only" htmlFor="archive-author">{t("archive.author")}</label><Input id="archive-author" list="archive-author-options" value={author} onChange={setAuthor} placeholder={t("所有用户")} showClear /><datalist id="archive-author-options">{authorOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</datalist></div>
      <label className="sr-only" id="archive-media-label" htmlFor="archive-media">{t("archive.mediaType")}</label><Select className="toolbar-select" id="archive-media" aria-labelledby="archive-media-label" value={filters.get("mediaType") ?? ""} onChange={value => change({ mediaType: String(value) })} optionList={[{ value: "", label: t("全部") }, { value: "text", label: t("archive.textOnly") }, { value: "photo", label: t("图片") }, { value: "video", label: t("视频") }, { value: "animated_gif", label: "GIF" }]} />
      <label className="sr-only" id="archive-sort-label" htmlFor="archive-sort">{t("排序顺序")}</label><Select className="toolbar-select" id="archive-sort" aria-labelledby="archive-sort-label" value={filters.get("sort") === "asc" ? "asc" : "desc"} onChange={value => change({ sort: String(value) })} optionList={[{ value: "desc", label: t("最新发布优先"), showTick: false }, { value: "asc", label: t("最早发布优先"), showTick: false }]} />
      <div className="archive-date">
        <Suspense fallback={<Button type="tertiary" aria-label={t("发布时间")} icon={<CalendarDays size={16} />}>{t("全部时间")}</Button>}><ArchiveDatePicker type="dateRange" format="yyyy-MM-dd" onChangeWithDateFirst={false} dropdownClassName="archive-date-picker"
          value={fromDate && toDate ? [fromDate, toDate] : []}
          onChange={value => { const dates = Array.isArray(value) ? value : []; change({ from: String(dates[0] ?? ""), to: String(dates[1] ?? "") }); }}
          triggerRender={() => <Button id="archive-date" type={fromDate || toDate ? "primary" : "tertiary"} aria-label={t("发布时间")} aria-pressed={Boolean(fromDate || toDate)} icon={<CalendarDays size={16} />}>{fromDate || toDate ? `${fromDate || t("不限")} ~ ${toDate || t("不限")}` : t("全部时间")}</Button>}
          showClear /></Suspense>
      </div>
      </div>
      </div>
    </div>
    {hasActiveFilters && <div className="archive-active-filters active-filter-tags">{(["id", "jobId", "author", "from", "to", "mediaType", "search", "retweets"] as const).map(key => [key, key === "from" ? fromDate : key === "to" ? toDate : key === "retweets" ? retweets : filters.get(key) ?? ""] as const).filter(([, value]) => Boolean(value)).map(([key, value]) => <Button size="small" type="tertiary" className="filter-chip" icon={<X size={12} />} iconPosition="right" key={key} aria-label={`${t("移除筛选")} ${value}`} onClick={() => { if (key === "search") setSearch(""); if (key === "author") setAuthor(""); change({ [key]: "" }); }}>{t(({ id: "推文 ID", jobId: "来源任务", author: "archive.author", from: "开始日期", to: "结束日期", mediaType: "archive.mediaType", search: "archive.search", retweets: "archive.retweetFilter" } as Record<string, string>)[key])}: {value}</Button>)}<Button size="small" theme="borderless" type="tertiary" onClick={clearAllFilters}>{t("archive.clearFilters")}</Button></div>}
    <div className="archive-results-bar">
      {page && <span className="archive-result-count" aria-live="polite">{t("archive.total", { count: page.pagination.total })}</span>}
    </div>
    {(tweets.isPlaceholderData || search !== debounced || author !== debouncedAuthor) && <div role="status" className="archive-query-status">{t("正在应用筛选...")}</div>}
    {newRecords > 0 && <Button className="archive-new-records" icon={<RefreshCw size={14} />} onClick={() => { if (tweets.data) setSnapshot({ key: queryIdentity, page: tweets.data }); }}>{t("有 {{count}} 条新记录", { count: newRecords })}</Button>}
    {tweets.isError && <Alert type="error" message={tweets.error.message} action={<Button onClick={() => void tweets.refetch()}>{t("重试")}</Button>} />}
    {!page && tweets.isLoading && <ListSkeleton />}
    {page?.items.length === 0 && <Empty description={hasActiveFilters ? t("没有匹配的推文") : t("archive.empty")}>{hasActiveFilters && <Button type="tertiary" onClick={clearAllFilters}>{t("archive.clearFilters")}</Button>}</Empty>}
    <div className="tweet-list">{page?.items.map(tweet => (
      <TweetCard
        key={tweet.id}
        tweet={tweet}
        compact={compact}
        highlight={debounced}
        onFilterAuthor={handleFilterAuthor}
        onOpenDetail={openTweetDetail}
        onOpenMedia={openTweetMedia}
        onMoreMedia={openTweetDetail}
        onRetry={handleRetryTweet}
        retryPending={retry.isPending && retry.variables === tweet.id}
      />
    ))}</div>
    {page && <Pagination page={page.pagination.page} pageSize={page.pagination.pageSize} total={page.pagination.total} onPageChange={value => change({ page: String(value) })} onPageSizeChange={value => change({ pageSize: String(value) })} />}
    <Drawer open={Boolean(detail)} onClose={() => setDetailId(null)} title={detail ? `@${detail.author?.screenName ?? ""}` : t("归档详情")} width="44rem">{detail && <div className="space-y-4"><div className="text-xs text-fg-muted">{t("推文 ID")}: {detail.id} · {formatDateTime(detail.createdAt)}</div><TweetText text={detailReposted?.text ?? detail.text} compact={false} highlight={debounced} />{detail.related?.filter(related => related.id !== detailReposted?.id).map(related => <blockquote className="tweet-related" key={related.id}><a href={`/tweets?id=${encodeURIComponent(related.id)}`}>@{related.author?.screenName ?? ""}</a><TweetText text={related.text} compact={false} highlight={debounced} /></blockquote>)}<div className="tweet-archive-meta">{t("归档于")} {formatDateTime(detail.archivedAt)}<span>{t("来源任务")}: {detail.jobIds.map(id => `#${id}`).join(", ") || "—"}</span></div>{detail.downloads.length > 0 && <div className="tweet-detail-files">{detail.downloads.map(record => <button type="button" className="tweet-detail-file" key={record.id} onClick={() => setPreview({ tweet: detail, index: Math.max(0, detail.downloads.findIndex(item => item.id === record.id)) })}><FileText size={16} /><span>{record.filePath.split(/[\\/]/).pop()}</span><ExternalLink size={14} /></button>)}</div>}<div className="tweet-detail-footer"><Button icon={<Copy size={16} />} onClick={() => void copyToClipboard(detail.url, t("推文链接"))}>{t("复制链接")}</Button>{/^https?:\/\//i.test(detail.url) && <a href={detail.url} target="_blank" rel="noreferrer" className="semi-button semi-button-light semi-button-tertiary semi-button-size-default"><ExternalLink size={14} /><span>{t("archive.original")}</span></a>}{detail.downloads[0] && <a href={`/gallery?q=${encodeURIComponent(detail.downloads[0].filePath.split(/[\\/]/).pop() || "")}`} className="semi-button semi-button-light semi-button-tertiary semi-button-size-default"><span>{t("定位媒体库")}</span></a>}</div></div>}</Drawer>
    <MediaModal open={preview !== null} onClose={() => setPreview(null)} title={preview ? `@${preview.tweet.author?.screenName ?? ""} · ${preview.tweet.id}` : ""}>{preview && previewRecord && <div className="space-y-3"><div className="relative flex min-h-[18rem] items-center justify-center bg-black/95 p-2"><Button theme="borderless" type="tertiary" icon={<ChevronLeft size={20} />} aria-label={t("上一个媒体")} disabled={preview.index <= 0} onClick={() => setPreview(current => current && current.index > 0 ? { ...current, index: current.index - 1 } : current)} className="gallery-preview-nav absolute left-2 z-10 text-white disabled:opacity-30" />{previewVideo ? <video key={previewRecord.id} className="max-h-[75dvh] max-w-full mx-auto" src={previewRecord.fileUrl ?? `/api/library/downloads/${previewRecord.id}/file`} poster={`/api/library/downloads/${previewRecord.id}/preview`} controls autoPlay playsInline /> : <img className="max-h-[75dvh] max-w-full mx-auto object-contain" src={previewRecord.fileUrl ?? `/api/library/downloads/${previewRecord.id}/file`} alt={`@${preview.tweet.author?.screenName ?? ""} · ${preview.tweet.id}`} />}<Button theme="borderless" type="tertiary" icon={<ChevronRight size={20} />} aria-label={t("下一个媒体")} disabled={preview.index >= preview.tweet.downloads.length - 1} onClick={() => setPreview(current => current && current.index < current.tweet.downloads.length - 1 ? { ...current, index: current.index + 1 } : current)} className="gallery-preview-nav absolute right-2 z-10 text-white disabled:opacity-30" /></div><div className="flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted"><span className="tabular-nums">{preview.index + 1} / {preview.tweet.downloads.length}</span>{preview.tweet.downloads.length > 1 && <span className="text-fg-subtle">{t("按 ← / → 切换")}</span>}<a className="text-brand-600" href={`/gallery?q=${encodeURIComponent(previewRecord.filePath.split(/[\\/]/).pop() || "")}`}>{t("定位媒体库")}</a></div></div>}</MediaModal>
  </div>;
}
