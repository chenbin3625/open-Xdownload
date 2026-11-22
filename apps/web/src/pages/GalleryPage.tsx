import { t, useTranslation } from "../lib/i18n";
import {
  CalendarDays,
  ChevronLeft,
  ChevronRight,
  Copy,
  Download,
  ExternalLink,
  Film,
  HardDrive,
  LayoutGrid,
  Link,
  List,
  Image as ImageIcon,
  PictureInPicture,
  Search,
  Users,
  Video,
  X,
} from "lucide-react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import React, { useCallback, useEffect, useMemo, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Card from "@douyinfe/semi-ui/lib/es/card";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import InputNumber from "@douyinfe/semi-ui/lib/es/inputNumber";
import RadioGroup from "@douyinfe/semi-ui/lib/es/radio/radioGroup";
import Select from "@douyinfe/semi-ui/lib/es/select";
import {
  formatBytes,
  getLibraryDownloadsPage,
  getLibraryUsers,
  libraryDownloadsQueryRoot,
  libraryUsersQueryRoot,
  type DownloadRecord,
  type LibraryUserSummary,
} from "../lib/api";
import { copyToClipboard, formatDateTime, getErrorMessage } from "../components/common/CommonUI";
import { smallTwimgUrl } from "../lib/tweetDisplay";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import {
  defaultGalleryFilter,
  galleryPageSize,
  type GalleryRouteState,
  type GallerySort,
} from "../lib/useRouteState";
import { Alert, Empty, Skeleton } from "../components/semi/feedback";
import { MediaModal, SimplePopover, Tooltip } from "../components/semi/overlays";
import { Pagination } from "../components/semi/data";
import { SearchSelect } from "../components/semi/controls";

const VIDEO_EXTENSIONS = [".mp4", ".mov", ".m4v", ".webm", ".ogv"];
const IMAGE_EXTENSIONS = [".jpg", ".jpeg", ".png", ".webp", ".avif"];

function hasExtension(url: string, extensions: string[]) {
  const path = url.toLowerCase().split(/[?#]/)[0];
  return extensions.some((ext) => path.endsWith(ext));
}

function displayFileName(item: DownloadRecord) {
  return item.filePath.split(/[\\/]/).pop() || item.filePath;
}

function fileURL(item: DownloadRecord) {
  return item.fileUrl || `/api/library/downloads/${item.id}/file`;
}

function entryKey(item: DownloadRecord) {
  return item.id > 0 ? String(item.id) : item.filePath;
}

function sourceTweetURL(item: DownloadRecord) {
  return item.tweetId ? `https://x.com/i/status/${encodeURIComponent(item.tweetId)}` : "";
}

// 焦点在 <video controls> 或可编辑元素上时，方向键属于该元素（视频拖动进度、文本移动光标），灯箱不应截获。
function isKeyboardOwnedTarget(target: EventTarget | null) {
  if (!(target instanceof Element)) return false;
  if (target instanceof HTMLMediaElement) return true;
  if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) {
    return true;
  }
  const editable = target.closest("[contenteditable]");
  return editable !== null && editable.getAttribute("contenteditable") !== "false";
}

export interface GalleryPageProps {
  /** 受控筛选（App 从 URL 还原并写回）；不传时组件内部自管，便于单独测试。 */
  filter?: GalleryRouteState;
  onFilterChange?: (
    patch: Partial<GalleryRouteState>,
    options?: { replace?: boolean },
  ) => void;
}

/** 搜索输入防抖：避免每敲一个字符就打一次服务端分页查询。 */
const GALLERY_SEARCH_DEBOUNCE_MS = 300;

function VideoPoster({
  item,
  fileName,
  onOpen,
  fit,
}: {
  item: DownloadRecord;
  fileName: string;
  onOpen: () => void;
  fit: "contain" | "cover";
}) {
  useTranslation();
  const sources = useMemo(() => {
    const list: { url: string; remote: boolean }[] = [];
    if (item.id > 0) {
      list.push({ url: `/api/library/downloads/${item.id}/preview`, remote: false });
    }
    if (item.previewUrl && !hasExtension(item.previewUrl, VIDEO_EXTENSIONS)) {
      list.push({ url: smallTwimgUrl(item.previewUrl) ?? item.previewUrl, remote: true });
    }
    return list;
  }, [item.previewUrl, item.id]);
  const [sourceIndex, setSourceIndex] = useState(0);

  return (
    <button
      type="button"
      className="group relative h-full w-full cursor-pointer border-0 bg-media-bg p-0 overflow-hidden"
      onClick={onOpen}
      aria-label={t("预览视频 {{value0}}", { value0: fileName })}
    >
      <div className="absolute inset-0 flex flex-col items-center justify-center gap-1.5 text-media-fg">
        <Video className="size-8 text-brand-400" />
        <span className="text-xs">{t("播放视频")}</span>
      </div>
      {sourceIndex < sources.length && (
        <img
          src={sources[sourceIndex].url}
          alt={fileName}
          loading="lazy"
          decoding="async"
          referrerPolicy={sources[sourceIndex].remote ? "no-referrer" : undefined}
          onError={() => setSourceIndex((current) => current + 1)}
          className={`relative z-10 h-full w-full ${fit === "cover" ? "object-cover" : "object-contain"} transition-transform duration-200 group-hover:scale-105`}
        />
      )}
      <div className="absolute inset-0 z-20 flex items-center justify-center bg-black/20 opacity-0 transition-opacity group-hover:opacity-100">
        <div className="flex size-10 items-center justify-center rounded-full bg-brand-500/90 text-brand-contrast shadow-md">
          <PictureInPicture className="size-5" />
        </div>
      </div>
    </button>
  );
}

const GalleryCard = React.memo(function GalleryCard({
  item,
  isVideo,
  isPreviewableImage,
  thumbnailFit,
  onOpen,
}: {
  item: DownloadRecord;
  isVideo: boolean;
  isPreviewableImage: boolean;
  thumbnailFit: "contain" | "cover";
  onOpen: (previewId: string) => void;
}) {
  useTranslation();
  const fileName = displayFileName(item);
  const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
  const previewURL = fileURL(item);
  // 缩略图优先走图床小图，失败再回退未登记文件或本地接口；下载链接仍指向原文件。
  const remoteThumbnail = useMemo(() => smallTwimgUrl(item.previewUrl), [item.previewUrl]);
  const localThumbnail = item.id > 0 ? `/api/library/downloads/${item.id}/file` : previewURL;
  const [imageSource, setImageSource] = useState(remoteThumbnail ?? localThumbnail);
  const [imageFailed, setImageFailed] = useState(false);
  useEffect(() => {
    setImageFailed(false);
    setImageSource(remoteThumbnail ?? localThumbnail);
  }, [remoteThumbnail, localThumbnail]);

  return (
    <Card
      shadows="hover"
      className="gallery-card group min-w-0"
      bodyStyle={{ padding: 12 }}
      cover={
        <div className="gallery-card-cover relative flex items-center justify-center overflow-hidden bg-surface-muted border-b border-line">
          {isVideo ? (
            <VideoPoster key={`${item.id}:${item.previewUrl || ""}`} item={item} fileName={fileName} fit={thumbnailFit} onOpen={() => onOpen(entryKey(item))} />
          ) : isPreviewableImage ? (
            <div className="relative h-full w-full overflow-hidden">
              {imageFailed ? (
                <div className="flex h-full flex-col items-center justify-center gap-1.5 text-fg-subtle">
                  <ImageIcon className="size-8" />
                  <span className="text-xs">{t("预览不可用")}</span>
                  <button type="button" className="text-xs text-brand-600 hover:underline" onClick={() => setImageFailed(false)}>
                    {t("重试预览")}
                  </button>
                </div>
              ) : (
                <button
                  type="button"
                  className="group/img relative block h-full w-full cursor-pointer border-0 bg-transparent p-0 overflow-hidden"
                  onClick={() => onOpen(entryKey(item))}
                  aria-label={t("查看图片 {{value0}}", { value0: fileName })}
                >
                  <div className="absolute inset-0 flex flex-col items-center justify-center gap-1 text-fg-subtle">
                    <ImageIcon className="size-8" />
                    <span className="text-xs text-fg-subtle">{ext}</span>
                  </div>
                  <img
                    src={imageSource}
                    alt={fileName}
                    loading="lazy"
                    decoding="async"
                    onError={() => { if (imageSource !== localThumbnail) setImageSource(localThumbnail); else setImageFailed(true); }}
                    className={`relative z-10 h-full w-full ${thumbnailFit === "cover" ? "object-cover" : "object-contain"}`}
                  />
                </button>
              )}
            </div>
          ) : (
            <div className="flex flex-col items-center gap-1 text-fg-subtle">
              <Film className="size-8" />
              <span className="text-xs font-medium">{ext} {t(" 文件")}</span>
            </div>
          )}

          <span className="pointer-events-none absolute top-2 right-2 z-20 rounded-sm bg-black/70 px-1.5 py-0.5 text-xs text-white tabular-nums">
            {ext} · {formatBytes(item.bytes)}
          </span>
        </div>
      }
    >
      <div className="gallery-card-body">
        <div className="truncate text-sm font-semibold text-fg" title={fileName}>
          {fileName}
        </div>

        <div className="flex items-center gap-1 truncate text-xs text-fg-muted">
          {(item.userName || !item.userScreenName) && (
            <span className="min-w-0 truncate">{item.userName || t("未识别用户")}</span>
          )}
          {item.userScreenName && (
            <span className="min-w-0 truncate">@{item.userScreenName}</span>
          )}
        </div>

        <div className="gallery-card-meta">
          {item.tweetId && <a className="text-brand-600" href={`/tweets?id=${item.tweetId}`}>{t("archive.title")}</a>}
          <time dateTime={item.createdAt} title={formatDateTime(item.createdAt)}>{formatDateTime(item.createdAt)}</time>
          <div className="gallery-card-actions">
            <Tooltip title={t("下载原文件")}>
              <a
                className="gallery-download semi-button semi-button-borderless semi-button-tertiary semi-button-size-default semi-button-with-icon-only"
                href={previewURL}
                download={fileName}
                aria-label={t("下载原文件")}
              >
                <Download size={16} />
              </a>
            </Tooltip>
            <Tooltip title={t("复制本地路径")}>
              <Button
                theme="borderless"
                type="tertiary"
                icon={<Copy size={16} />}
                onClick={() => void copyToClipboard(item.filePath, t("本地路径"))}
                aria-label={t("复制本地路径")}
              />
            </Tooltip>
            <Tooltip title={t("复制媒体直链")}>
              <Button
                theme="borderless"
                type="tertiary"
                icon={<Link size={16} />}
                onClick={() => void copyToClipboard(item.mediaUrl, t("原始下载直链"))}
                aria-label={t("复制下载直链")}
              />
            </Tooltip>
            {sourceTweetURL(item) && (
              <Tooltip title={t("打开来源推文")}>
                <a
                  className="gallery-source-link semi-button semi-button-borderless semi-button-tertiary semi-button-size-default semi-button-with-icon-only"
                  href={sourceTweetURL(item)}
                  target="_blank"
                  rel="noreferrer"
                  aria-label={t("打开来源推文")}
                >
                  <ExternalLink size={16} />
                </a>
              </Tooltip>
            )}
          </div>
        </div>
      </div>
    </Card>
  );
});

/** 账号卡片：用头像 + X 名字区分不同归档账号，点击进入该账号的媒体列表。 */
const UserCard = React.memo(function UserCard({
  summary,
  onOpen,
}: {
  summary: LibraryUserSummary;
  onOpen: (screenName: string) => void;
}) {
  useTranslation();
  const initial = (summary.displayName || summary.screenName).trim().charAt(0).toUpperCase();
  return (
    <Card shadows="hover" className="gallery-card min-w-0" bodyStyle={{ padding: 12 }}>
      <button
        type="button"
        onClick={() => onOpen(summary.screenName)}
        className="flex w-full cursor-pointer items-center gap-3 border-0 bg-transparent p-0 text-left"
        aria-label={t("查看 @{{value0}} 的媒体", { value0: summary.screenName })}
      >
        {summary.avatarUrl ? (
          <img
            src={summary.avatarUrl}
            alt=""
            loading="lazy"
            decoding="async"
            referrerPolicy="no-referrer"
            className="size-12 shrink-0 rounded-full border border-line object-cover"
          />
        ) : (
          <span className="flex size-12 shrink-0 items-center justify-center rounded-full border border-line bg-surface-muted text-base font-semibold text-fg-muted">
            {initial || <Users className="size-5" />}
          </span>
        )}
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-semibold text-fg">{summary.displayName || summary.screenName}</span>
          <span className="block truncate text-xs text-fg-muted">@{summary.screenName}</span>
        </span>
      </button>
      <div className="gallery-card-meta mt-3">
        <span>{t("{{value0}} 个媒体 · {{value1}}", { value0: summary.mediaCount, value1: formatBytes(summary.totalBytes) })}</span>
        <time>{formatDateTime(summary.latestAt)}</time>
      </div>
    </Card>
  );
});

export function GalleryPage({ filter: controlledFilter, onFilterChange }: GalleryPageProps) {
  useTranslation();
  const [localFilter, setLocalFilter] = useState<GalleryRouteState>(defaultGalleryFilter);
  // 归档库的两种浏览方式：媒体网格 / 按账号卡片。
  const [viewMode, setViewMode] = useState<"media" | "users">("media");
  const filter = controlledFilter ?? localFilter;
  const updateFilter = useCallback(
    (patch: Partial<GalleryRouteState>, options?: { replace?: boolean }) => {
      if (controlledFilter !== undefined) {
        onFilterChange?.(patch, options);
        return;
      }
      setLocalFilter((current) => ({ ...current, ...patch }));
    },
    [controlledFilter, onFilterChange],
  );

  const [previewId, setPreviewId] = useState<string | null>(null);
  const [view, setView] = useState(() => { try { return localStorage.getItem("gallery-view") === "list" ? "list" : "grid"; } catch { return "grid"; } });
  const [density, setDensity] = useState(() => { try { return localStorage.getItem("gallery-density") === "compact" ? "compact" : "standard"; } catch { return "standard"; } });
  const [thumbnailFit, setThumbnailFit] = useState<"contain" | "cover">(() => { try { return localStorage.getItem("gallery-thumbnail-fit") === "cover" ? "cover" : "contain"; } catch { return "contain"; } });
  const [selected, setSelected] = useState<Set<string>>(new Set());
  useEffect(() => { try { localStorage.setItem("gallery-view", view); localStorage.setItem("gallery-density", density); localStorage.setItem("gallery-thumbnail-fit", thumbnailFit); } catch { /* Storage may be unavailable in private browsing. */ } }, [view, density, thumbnailFit]);
  useEffect(() => { setSelected(new Set()); }, [filter.page, filter.kind, filter.user, filter.search, filter.sort, filter.from, filter.to, filter.minBytes, filter.maxBytes]);
  const pageSize = galleryPageSize;
  // 输入框即时响应，查询键用 300ms 防抖后的值。
  const debouncedSearch = useDebouncedValue(filter.search, GALLERY_SEARCH_DEBOUNCE_MS);
  const rangeError = filter.from && filter.to && filter.from > filter.to
    ? t("开始日期不能晚于结束日期")
    : filter.minBytes !== "" && filter.maxBytes !== "" && Number(filter.minBytes) > Number(filter.maxBytes)
      ? t("最小体积不能大于最大体积") : "";

  // 只有切到「按账号」时才拉聚合数据，避免每次进归档库都多一次全量扫描。
  const usersQuery = useQuery({
    queryKey: libraryUsersQueryRoot,
    queryFn: getLibraryUsers,
    enabled: viewMode === "users",
    staleTime: 30_000,
  });

  const libraryQuery = useQuery({
    queryKey: [
      ...libraryDownloadsQueryRoot,
      {
        page: filter.page,
        kind: filter.kind,
        user: filter.user,
        search: debouncedSearch,
        sort: filter.sort,
        from: filter.from,
        to: filter.to,
        minBytes: filter.minBytes,
        maxBytes: filter.maxBytes,
      },
    ],
    queryFn: ({ signal }) =>
      getLibraryDownloadsPage(
        {
          page: filter.page,
          pageSize,
          kind: filter.kind,
          user: filter.user,
          search: debouncedSearch,
          sort: filter.sort,
          from: filter.from,
          to: filter.to,
          minBytes: filter.minBytes,
          maxBytes: filter.maxBytes,
        },
        signal,
      ),
    // 翻页/切筛选时保留上一页内容，避免整块骨架闪烁与分页条消失。
    placeholderData: keepPreviousData,
    enabled: !rangeError,
    staleTime: 10 * 60_000,
    gcTime: 30 * 60_000,
    refetchOnWindowFocus: false,
  });

  const allDownloads = libraryQuery.data?.items ?? [];

  const indexedDownloads = useMemo(() => {
    const groups = new Map<string, DownloadRecord[]>();
    for (const item of allDownloads) {
      const key = item.filePath || `record:${item.id}`;
      const group = groups.get(key);
      if (group) group.push(item);
      else groups.set(key, [item]);
    }
    return [...groups.values()].map((records) => {
      const item = records[0];
      const lower = item.filePath.toLowerCase().split("?")[0];
      return {
        item,
        records,
        isVideo: VIDEO_EXTENSIONS.some((ext) => lower.endsWith(ext)),
        isImage: IMAGE_EXTENSIONS.some((ext) => lower.endsWith(ext)),
        isGif: lower.endsWith(".gif"),
      };
    });
  }, [allDownloads]);

  const categoryCounts = libraryQuery.data?.counts ?? { all: 0, images: 0, videos: 0, gifs: 0 };
  const userOptions = libraryQuery.data?.users ?? [];
  const totalEntries = libraryQuery.data?.pagination.total ?? 0;
  const totalPages = Math.max(
    1,
    libraryQuery.data?.pagination.totalPages ?? Math.ceil(totalEntries / pageSize),
  );

  // 数据变少（删除/筛选后总数下降）时把页码收敛到最后一页，避免停在空页。
  useEffect(() => {
    if (!libraryQuery.data || libraryQuery.isPlaceholderData || debouncedSearch !== filter.search) return;
    if (filter.page > totalPages) {
      updateFilter({ page: totalPages }, { replace: true });
    }
  }, [libraryQuery.data, libraryQuery.isPlaceholderData, debouncedSearch, filter.search, filter.page, totalPages, updateFilter]);

  const visibleEntries = indexedDownloads;
  const selectedEntries = useMemo(
    () => visibleEntries.filter(({ item }) => selected.has(entryKey(item))),
    [selected, visibleEntries],
  );
  const selectionDisabled = libraryQuery.isPlaceholderData || Boolean(rangeError) || debouncedSearch !== filter.search;
  const hasActiveFilter =
    filter.kind !== "all" || filter.user !== "all" || filter.search.trim() !== "" ||
    filter.from !== "" || filter.to !== "" || filter.minBytes !== "" || filter.maxBytes !== "";

  const downloadSelected = useCallback(() => {
    selectedEntries.forEach(({ item }) => {
      const anchor = document.createElement("a");
      anchor.href = fileURL(item);
      anchor.download = displayFileName(item);
      anchor.rel = "noreferrer";
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
    });
  }, [selectedEntries]);

  const exportSelected = useCallback(() => {
    const payload = selectedEntries.map(({ item }) => ({
      id: item.id,
      filePath: item.filePath,
      fileUrl: fileURL(item),
      mediaUrl: item.mediaUrl,
      tweetId: item.tweetId,
      createdAt: item.createdAt,
    }));
    const url = URL.createObjectURL(new Blob([JSON.stringify(payload, null, 2)], { type: "application/json" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `open-xdownload-media-${new Date().toISOString().slice(0, 10)}.json`;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  }, [selectedEntries]);

  const previewIndex = useMemo(
    () =>
      previewId === null
        ? -1
        : visibleEntries.findIndex((entry) => entryKey(entry.item) === previewId),
    [previewId, visibleEntries],
  );
  const previewEntry = previewIndex >= 0 ? visibleEntries[previewIndex] : undefined;


  const openPreview = useCallback((nextId: string) => setPreviewId(nextId), []);
  const openUserMedia = useCallback((screenName: string) => {
    setViewMode("media");
    updateFilter({ user: screenName, page: 1 });
  }, [updateFilter]);

  // 跨页切图：走到当前页边界时翻一页，等新页数据落地后再把灯箱定位到首/末项。
  const [pendingPreviewEdge, setPendingPreviewEdge] = useState<"first" | "last" | null>(null);

  const stepPreview = useCallback(
    (delta: number) => {
      if (visibleEntries.length === 0 || previewIndex === -1 || pendingPreviewEdge || libraryQuery.isPlaceholderData) return;
      const nextIndex = previewIndex + delta;
      if (nextIndex >= visibleEntries.length) {
        if (filter.page < totalPages) {
          setPendingPreviewEdge("first");
          updateFilter({ page: filter.page + 1 });
        }
        return;
      }
      if (nextIndex < 0) {
        if (filter.page > 1) {
          setPendingPreviewEdge("last");
          updateFilter({ page: filter.page - 1 });
        }
        return;
      }
      setPreviewId(entryKey(visibleEntries[nextIndex].item));
    },
    [filter.page, previewIndex, totalPages, updateFilter, visibleEntries, pendingPreviewEdge, libraryQuery.isPlaceholderData],
  );

  useEffect(() => {
    if (!pendingPreviewEdge) return;
    if (libraryQuery.isError) {
      setPendingPreviewEdge(null);
      return;
    }
    if (!libraryQuery.data || libraryQuery.isPlaceholderData) return;
    if (visibleEntries.length === 0) {
      setPendingPreviewEdge(null);
      return;
    }
    const target =
      pendingPreviewEdge === "first"
        ? visibleEntries[0]
        : visibleEntries[visibleEntries.length - 1];
    setPreviewId(entryKey(target.item));
    setPendingPreviewEdge(null);
  }, [pendingPreviewEdge, libraryQuery.data, libraryQuery.isPlaceholderData, visibleEntries]);

  useEffect(() => {
    // 正在跨页续开时不要因为"当前页找不到旧 previewId"就关掉灯箱。
    if (previewId !== null && previewIndex === -1 && !pendingPreviewEdge) {
      setPreviewId(null);
      return;
    }
    if (previewIndex === -1) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (isKeyboardOwnedTarget(event.target)) return;
      if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        event.preventDefault();
        stepPreview(event.key === "ArrowRight" ? 1 : -1);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [pendingPreviewEdge, previewId, previewIndex, stepPreview]);

  const goToPage = useCallback(
    (nextPage: number) => {
      setPreviewId(null);
      updateFilter({ page: Math.min(Math.max(1, nextPage), totalPages) });
    },
    [totalPages, updateFilter],
  );

  return (
    <div className="workspace-page gallery-page">
      <div className="page-heading">
        <h1 className="page-title">{t("媒体归档库")}</h1>
        <div className="page-actions">
          <RadioGroup
            type="button"
            buttonSize="middle"
            aria-label={t("浏览方式")}
            value={viewMode}
            onChange={(event) => setViewMode(String(event.target.value) as "media" | "users")}
            options={[
              { label: t("媒体"), value: "media" },
              { label: t("按账号"), value: "users" },
            ]}
          />
          {viewMode === "media" && <>
            <RadioGroup type="button" buttonSize="middle" aria-label={t("媒体视图")} value={view} onChange={event => setView(event.target.value)} options={[{ value: "grid", label: <span title={t("网格视图")} aria-label={t("网格视图")}><LayoutGrid size={16} /></span> }, { value: "list", label: <span title={t("列表视图")} aria-label={t("列表视图")}><List size={16} /></span> }]} />
            {view === "grid" && <><label id="gallery-density-label" className="sr-only">{t("缩略图大小")}</label><Select value={density} aria-labelledby="gallery-density-label" onChange={value => setDensity(String(value))} optionList={[{ value: "standard", label: t("标准缩略图") }, { value: "compact", label: t("紧凑缩略图") }]} /><label id="gallery-fit-label" className="sr-only">{t("缩略图显示方式")}</label><Select value={thumbnailFit} aria-labelledby="gallery-fit-label" onChange={value => setThumbnailFit(value as "contain" | "cover")} optionList={[{ value: "contain", label: t("完整显示") }, { value: "cover", label: t("填充裁剪") }]} /></>}
          </>}
        </div>
      </div>
      {viewMode === "media" && <>
      <div className="page-toolbar gallery-toolbar">
        <div className="toolbar-status" role="radiogroup" aria-label={t("类型")}>
          <RadioGroup
            type="button"
            buttonSize="middle"
            value={filter.kind}
            onChange={(event) => updateFilter({ kind: String(event.target.value), page: 1 })}
            options={[
              { label: t("全部 ({{value0}})", { value0: categoryCounts.all }), value: "all" },
              { label: t("图片 ({{value0}})", { value0: categoryCounts.images }), value: "images" },
              { label: t("视频 ({{value0}})", { value0: categoryCounts.videos }), value: "videos" },
              { label: t("动图 ({{value0}})", { value0: categoryCounts.gifs }), value: "gifs" },
            ]}
          />
        </div>

        <div className="toolbar-filters">
          <Input
            prefix={<Search size={16} />}
            aria-label={t("搜索文件名、推文 ID...")}
            placeholder={t("搜索文件名、推文 ID...")}
            value={filter.search}
            onChange={(value) => updateFilter({ search: value, page: 1 }, { replace: true })}
            showClear
            className="toolbar-search"
          />

          <SearchSelect
            ariaLabel={t("按用户筛选...")}
            value={filter.user}
            onChange={(val) => updateFilter({ user: val, page: 1 })}
            options={[{ value: "all", label: t("全部用户") }, ...userOptions]}
            placeholder={t("按用户筛选...")}
            className="toolbar-user-select"
          />
          <label id="gallery-sort-label" className="sr-only">{t("排序顺序")}</label>
          <Select
            aria-labelledby="gallery-sort-label"
            value={filter.sort}
            onChange={value => updateFilter({ sort: value as GallerySort, page: 1 })}
            className="toolbar-select gallery-sort"
            optionList={[
              { value: "date_desc", label: t("最新归档优先") },
              { value: "date_asc", label: t("最早归档优先") },
              { value: "size_desc", label: t("体积从大到小") },
              { value: "size_asc", label: t("体积从小到大") },
            ]}
          />
        <div className="toolbar-options">
        <SimplePopover trigger={<Button
          icon={<CalendarDays size={16} />}
          theme="light"
          type={filter.from || filter.to ? "primary" : "tertiary"}
          aria-label={t("归档日期（UTC）")}
          aria-pressed={Boolean(filter.from || filter.to)}
          title={`${t("归档日期（UTC）")}: ${filter.from || t("不限")} ~ ${filter.to || t("不限")}`}
        />}>
        <div className="toolbar-field gallery-range-panel">
          <label id="gallery-date-label">{t("归档日期（UTC）")}</label>
          <div className="gallery-range-inputs" role="group" aria-labelledby="gallery-date-label">
            <Input type="date" aria-label={t("开始日期")} value={filter.from} max={filter.to || undefined} onChange={value => updateFilter({ from: value, page: 1 })} showClear />
            <span className="text-fg-subtle">~</span>
            <Input type="date" aria-label={t("结束日期")} value={filter.to} min={filter.from || undefined} onChange={value => updateFilter({ to: value, page: 1 })} showClear />
          </div>
        </div>
        </SimplePopover>
        <SimplePopover trigger={<Button
          icon={<HardDrive size={16} />}
          theme="light"
          type={filter.minBytes !== "" || filter.maxBytes !== "" ? "primary" : "tertiary"}
          aria-label={t("文件体积（MiB）")}
          aria-pressed={filter.minBytes !== "" || filter.maxBytes !== ""}
          title={`${t("文件体积（MiB）")}: ${filter.minBytes === "" ? t("不限") : formatBytes(Number(filter.minBytes))} ~ ${filter.maxBytes === "" ? t("不限") : formatBytes(Number(filter.maxBytes))}`}
        />}>
        <div className="toolbar-field gallery-range-panel">
          <label id="gallery-size-label">{t("文件体积（MiB）")}</label>
          <div className="gallery-range-inputs" role="group" aria-labelledby="gallery-size-label">
            {(["minBytes", "maxBytes"] as const).map((key, index) => <React.Fragment key={key}>
              {index > 0 && <span className="text-fg-subtle">~</span>}
              <InputNumber
                aria-label={t(index === 0 ? "最小体积" : "最大体积")}
                placeholder={t("不限")}
                min={0} max={Number.MAX_SAFE_INTEGER / 1048576}
                value={filter[key] === "" ? "" : Number((Number(filter[key]) / 1048576).toFixed(6))}
                onChange={value => {
                  if (value !== "" && (!Number.isFinite(Number(value)) || Number(value) < 0)) return;
                  updateFilter({ [key]: value === "" ? "" : String(Math.round(Number(value) * 1048576)), page: 1 }, { replace: true });
                }}
              />
            </React.Fragment>)}
          </div>
        </div>
        </SimplePopover>
        {hasActiveFilter && <Tooltip title={t("清除筛选")}><Button theme="borderless" type="tertiary" icon={<X size={16} />} aria-label={t("清除筛选")} onClick={() => updateFilter({ kind: "all", user: "all", search: "", from: "", to: "", minBytes: "", maxBytes: "", page: 1 })} /></Tooltip>}
        </div>
        </div>
      </div>
      {hasActiveFilter && <div className="active-filter-tags">
        {[
          { key: "search", label: filter.search, patch: { search: "" } },
          { key: "kind", label: filter.kind !== "all" ? filter.kind === "images" ? t("图片") : filter.kind === "videos" ? t("视频") : t("动图") : "", patch: { kind: "all" as const } },
          { key: "user", label: filter.user !== "all" ? filter.user : "", patch: { user: "all" } },
          { key: "dates", label: filter.from || filter.to ? `${filter.from || t("不限")} ~ ${filter.to || t("不限")}` : "", patch: { from: "", to: "" } },
          { key: "bytes", label: filter.minBytes || filter.maxBytes ? `${filter.minBytes ? formatBytes(Number(filter.minBytes)) : t("不限")} ~ ${filter.maxBytes ? formatBytes(Number(filter.maxBytes)) : t("不限")}` : "", patch: { minBytes: "", maxBytes: "" } },
        ].filter(item => item.label).map(item => <Button className="filter-chip" size="small" type="tertiary" icon={<X size={12} />} iconPosition="right" key={item.key} onClick={() => updateFilter({ ...item.patch, page: 1 })} aria-label={`${t("移除筛选")} ${item.label}`}>{item.label}</Button>)}
      </div>}
      <div className="gallery-result-bar">
        <Checkbox checked={Boolean(visibleEntries.length) && selectedEntries.length === visibleEntries.length} indeterminate={selectedEntries.length > 0 && selectedEntries.length < visibleEntries.length} disabled={selectionDisabled || !visibleEntries.length} onChange={event => setSelected(event.target.checked ? new Set(visibleEntries.map(({ item }) => entryKey(item))) : new Set())}>{t("选择本页")}</Checkbox>
        <span className="text-xs text-fg-muted" aria-live="polite">{libraryQuery.isPlaceholderData ? t("正在应用筛选...") : `${t("筛选结果")} ${totalEntries} ${t("项")}`}</span>
        {selectedEntries.length > 0 && <div className="gallery-batch-actions"><span className="text-xs">{t("已选")} {selectedEntries.length}</span><Button size="small" type="tertiary" icon={<Copy size={14} />} disabled={selectionDisabled} onClick={() => void copyToClipboard(selectedEntries.map(({ item }) => item.filePath).join("\n"), t("所选文件路径"))}>{t("复制路径")}</Button><Button size="small" type="tertiary" icon={<Download size={14} />} disabled={selectionDisabled} onClick={downloadSelected}>{t("批量下载")}</Button><Button size="small" type="tertiary" icon={<ExternalLink size={14} />} disabled={selectionDisabled} onClick={exportSelected}>{t("导出清单")}</Button><Button size="small" theme="borderless" type="tertiary" icon={<X size={14} />} aria-label={t("取消选择")} onClick={() => setSelected(new Set())} /></div>}
      </div>
      {rangeError && <Alert type="warning" message={rangeError} />}
      {libraryQuery.isError && libraryQuery.data && <Alert type="error" message={getErrorMessage(libraryQuery.error)} />}

      {libraryQuery.data?.truncated && (
        <Alert type="warning" message={t("媒体库索引已达上限，当前仅显示最近的记录")} />
      )}
      </>}

      {/* 按账号浏览：头像 + 名字的卡片网格，点卡片回到该账号的媒体列表 */}
      {viewMode === "users" ? (
        usersQuery.isLoading ? (
          <div className="gallery-grid">
            {Array.from({ length: 8 }, (_, index) => (
              <Card key={index} bodyStyle={{ padding: 12 }}>
                <div className="flex items-center gap-3">
                  <Skeleton className="size-12 rounded-full" />
                  <div className="flex-1 space-y-2">
                    <Skeleton className="h-3 w-3/5" />
                    <Skeleton className="h-2.5 w-2/5" />
                  </div>
                </div>
              </Card>
            ))}
          </div>
        ) : usersQuery.isError ? (
          <Alert
            type="error"
            showIcon
            message={t("加载账号列表失败")}
            description={getErrorMessage(usersQuery.error)}
            action={
              <Button
                type="tertiary"
                loading={usersQuery.isFetching}
                disabled={usersQuery.isFetching}
                aria-busy={usersQuery.isFetching}
                onClick={() => void usersQuery.refetch()}
              >
                {t("重试")}
              </Button>
            }
          />
        ) : (usersQuery.data?.items.length ?? 0) === 0 ? (
          <Empty description={t("还没有可浏览的账号")} />
        ) : (
          <div className="gallery-grid">
            {(usersQuery.data?.items ?? []).map((summary) => (
              <UserCard
                key={summary.screenName}
                summary={summary}
                onOpen={openUserMedia}
              />
            ))}
          </div>
        )
      ) : (
        <>
      {/* 媒体内容流展示 */}
      {libraryQuery.isLoading ? (
        <div className="gallery-grid">
          {Array.from({ length: 12 }, (_, index) => (
            <Card key={index} bodyStyle={{ padding: 12 }}>
              <div className="space-y-2">
              <Skeleton className="aspect-square w-full rounded" />
              <Skeleton className="h-3 w-4/5" />
              <Skeleton className="h-2.5 w-1/2" />
              </div>
            </Card>
          ))}
        </div>
      ) : libraryQuery.isError && !libraryQuery.data ? (
          <Alert
            type="error"
            showIcon
            message={t("媒体库加载失败")}
            description={getErrorMessage(libraryQuery.error)}
            action={
              <Button
                type="tertiary"
                loading={libraryQuery.isFetching}
                disabled={libraryQuery.isFetching}
                aria-busy={libraryQuery.isFetching}
                onClick={() => void libraryQuery.refetch()}
              >
                {t("重试")}</Button>
            }
          />
      ) : visibleEntries.length === 0 ? (
          <Empty
            description={
              hasActiveFilter
                ? t("没有匹配的媒体文件，试试调整筛选或搜索关键词。")
                : t("暂无归档文件记录。完成下载任务后，媒体文件将自动在此呈现。")
            }
          />
      ) : (
        <div
          className={`gallery-${view} gallery-density-${density}${libraryQuery.isFetching ? " opacity-60 transition-opacity" : ""}`}
          aria-busy={libraryQuery.isFetching}
        >
          {visibleEntries.map(({ item, isVideo, isImage, isGif }) => (
            <div key={entryKey(item)} className={`gallery-entry${selected.has(entryKey(item)) ? " gallery-entry-selected" : ""}`}>
            <Checkbox className="gallery-entry-checkbox" aria-label={`${t("选择文件")} ${displayFileName(item)}`} checked={selected.has(entryKey(item))} disabled={selectionDisabled} onChange={event => setSelected(current => { const next = new Set(current); if (event.target.checked) next.add(entryKey(item)); else next.delete(entryKey(item)); return next; })} />
            <GalleryCard
              key={item.id || item.filePath}
              item={item}
              isVideo={isVideo}
              isPreviewableImage={isImage || isGif}
              thumbnailFit={thumbnailFit}
              onOpen={openPreview}
            />
            </div>
          ))}
        </div>
      )}

      {/* 底部分页（服务端分页，页码写回 URL） */}
      {totalPages > 1 && (
        <div className="flex justify-center pt-3">
          <Pagination
            page={filter.page}
            pageSize={pageSize}
            total={totalEntries}
            totalLabel={(total) => t("共 {{value0}} 项", { value0: total })}
            onPageChange={goToPage}
          />
        </div>
      )}

        </>
      )}

      {/* 沉浸式多媒体灯箱预览弹窗 */}
      <MediaModal
        open={previewEntry !== undefined}
        onClose={() => {
          setPendingPreviewEdge(null);
          setPreviewId(null);
        }}
        title={
          previewEntry
            ? [previewEntry.item.userName, previewEntry.item.userScreenName ? `@${previewEntry.item.userScreenName}` : ""].filter(Boolean).join("  ") || t("未识别用户")
            : ""
        }
      >
        {previewEntry && (() => {
          const { item, isVideo } = previewEntry;
          const fileName = displayFileName(item);
          const previewURL = fileURL(item);

          return (
            <div className="flex flex-col">
              <div className="gallery-preview-frame relative flex items-center justify-center bg-black/95 p-2 overflow-hidden">
                {isVideo ? (
                  <video
                    key={entryKey(item)}
                    src={previewURL}
                    controls
                    autoPlay
                    playsInline
                    className="h-full w-full min-h-0 min-w-0 object-contain"
                  />
                ) : (
                  <img
                    src={previewURL}
                    alt={fileName}
                    className="h-full w-full min-h-0 min-w-0 object-contain"
                  />
                )}

                {/* 左右切图浮动按钮（当前服务端分页内循环） */}
                <button
                  type="button"
                  aria-label={t("上一个媒体文件")}
                  disabled={filter.page === 1 && previewIndex === 0 || Boolean(pendingPreviewEdge)}
                  onClick={() => stepPreview(-1)}
                  className="absolute left-3 top-1/2 -translate-y-1/2 flex size-10 items-center justify-center rounded-full bg-black/60 text-white hover:bg-black/90 transition-colors cursor-pointer"
                >
                  <ChevronLeft className="size-5" />
                </button>
                <button
                  type="button"
                  aria-label={t("下一个媒体文件")}
                  disabled={filter.page === totalPages && previewIndex === visibleEntries.length - 1 || Boolean(pendingPreviewEdge)}
                  onClick={() => stepPreview(1)}
                  className="absolute right-3 top-1/2 -translate-y-1/2 flex size-10 items-center justify-center rounded-full bg-black/60 text-white hover:bg-black/90 transition-colors cursor-pointer"
                >
                  <ChevronRight className="size-5" />
                </button>
              </div>
              <div className="gallery-preview-details"><span>{pendingPreviewEdge || libraryQuery.isPlaceholderData ? t("加载中...") : `${((libraryQuery.data?.pagination.page ?? filter.page) - 1) * pageSize + previewIndex + 1} / ${totalEntries}`}</span>{item.jobId > 0 && <a href="/tasks">{t("来源任务")} #{item.jobId}</a>}{item.tweetId && <a href={`/tweets?id=${item.tweetId}`}>{t("archive.title")} <ExternalLink size={12} /></a>}<code title={item.filePath}>{item.filePath}</code></div>

              {/* 弹窗底部操作与详情栏 */}
              <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line bg-surface px-4 py-3 text-xs">
                <div className="w-full min-w-0 sm:w-auto sm:max-w-md">
                  <div className="font-semibold text-fg truncate" title={fileName}>
                    {fileName}
                  </div>
                  <div className="text-xs text-fg-muted tabular-nums truncate">
                    {formatBytes(item.bytes)} · {formatDateTime(item.createdAt)}
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  <Tooltip title={t("下载原文件")}>
                    <a
                      className="gallery-download semi-button semi-button-borderless semi-button-tertiary semi-button-size-default semi-button-with-icon-only"
                      href={previewURL}
                      download={fileName}
                      aria-label={t("下载原文件")}
                    >
                      <Download size={16} />
                    </a>
                  </Tooltip>
                  <Tooltip title={t("复制本地路径")}>
                    <Button
                      theme="borderless"
                      type="tertiary"
                      icon={<Copy size={16} />}
                      aria-label={t("复制本地路径")}
                      onClick={() => void copyToClipboard(item.filePath, t("本地路径"))}
                    />
                  </Tooltip>
                  <Tooltip title={t("复制直链")}>
                    <Button
                      theme="borderless"
                      type="tertiary"
                      icon={<Link size={16} />}
                      aria-label={t("复制直链")}
                      onClick={() => void copyToClipboard(item.mediaUrl, t("原始下载直链"))}
                    />
                  </Tooltip>
                  {sourceTweetURL(item) && (
                    <Tooltip title={t("打开来源推文")}>
                      <a
                        className="gallery-source-link semi-button semi-button-borderless semi-button-tertiary semi-button-size-default semi-button-with-icon-only"
                        href={sourceTweetURL(item)}
                        target="_blank"
                        rel="noreferrer"
                        aria-label={t("打开来源推文")}
                      >
                        <ExternalLink size={16} />
                      </a>
                    </Tooltip>
                  )}
                </div>
              </div>
            </div>
          );
        })()}
      </MediaModal>
    </div>
  );
}
