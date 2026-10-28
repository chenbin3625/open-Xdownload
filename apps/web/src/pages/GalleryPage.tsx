import {
  ChevronLeft,
  ChevronRight,
  Copy,
  ExternalLink,
  Film,
  Image as ImageIcon,
  PictureInPicture,
  Video,
} from "lucide-react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  formatBytes,
  getLibraryDownloadsPage,
  libraryDownloadsQueryRoot,
  type DownloadRecord,
} from "../lib/api";
import { copyToClipboard, formatDateTime, getErrorMessage } from "../components/common/CommonUI";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import {
  defaultGalleryFilter,
  galleryPageSize,
  type GalleryRouteState,
} from "../lib/useRouteState";
import { Button } from "../components/ui/Button";
import { Card } from "../components/ui/Card";
import { Segmented } from "../components/ui/Controls";
import { Alert, Empty, Skeleton } from "../components/ui/Feedback";
import { SearchInput } from "../components/ui/Input";
import { MediaModal, Tooltip } from "../components/ui/Overlay";
import { Pagination } from "../components/ui/Pagination";
import { SearchSelect } from "../components/ui/SearchSelect";

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
}: {
  item: DownloadRecord;
  fileName: string;
  onOpen: () => void;
}) {
  const sources = useMemo(() => {
    const list: { url: string; remote: boolean }[] = [];
    if (item.id > 0) {
      list.push({ url: `/api/library/downloads/${item.id}/preview`, remote: false });
    }
    if (item.previewUrl && !hasExtension(item.previewUrl, VIDEO_EXTENSIONS)) {
      list.push({ url: item.previewUrl, remote: true });
    }
    return list;
  }, [item.previewUrl, item.id]);
  const [sourceIndex, setSourceIndex] = useState(0);

  return (
    <button
      type="button"
      className="group relative h-full w-full cursor-pointer border-0 bg-slate-950 p-0 overflow-hidden"
      onClick={onOpen}
      aria-label={`预览视频 ${fileName}`}
    >
      <div className="absolute inset-0 flex flex-col items-center justify-center gap-1.5 text-slate-400">
        <Video className="size-8 text-brand-400" />
        <span className="text-xs">播放视频</span>
      </div>
      {sourceIndex < sources.length && (
        <img
          src={sources[sourceIndex].url}
          alt={fileName}
          loading="lazy"
          decoding="async"
          referrerPolicy={sources[sourceIndex].remote ? "no-referrer" : undefined}
          onError={() => setSourceIndex((current) => current + 1)}
          className="relative z-10 h-full w-full object-contain transition-transform duration-200 group-hover:scale-105"
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
  onOpen,
}: {
  item: DownloadRecord;
  isVideo: boolean;
  isPreviewableImage: boolean;
  onOpen: (previewId: string) => void;
}) {
  const fileName = displayFileName(item);
  const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
  const previewURL = fileURL(item);

  return (
    <Card
      hoverable
      size="sm"
      className="group rounded-2xl border-line/80 hover:border-brand-500/40 hover:-translate-y-1 transition-all duration-300 shadow-xs hover:shadow-raised"
      bodyClassName="p-3"
      cover={
        <div className="aspect-square relative flex items-center justify-center overflow-hidden bg-surface-muted border-b border-line">
          {isVideo ? (
            <VideoPoster key={`${item.id}:${item.previewUrl || ""}`} item={item} fileName={fileName} onOpen={() => onOpen(entryKey(item))} />
          ) : isPreviewableImage ? (
            <button
              type="button"
              className="group/img relative block h-full w-full cursor-pointer border-0 bg-transparent p-0 overflow-hidden"
              onClick={() => onOpen(entryKey(item))}
              aria-label={`查看图片 ${fileName}`}
            >
              <div className="absolute inset-0 flex flex-col items-center justify-center gap-1 text-fg-subtle">
                <ImageIcon className="size-8" />
                <span className="text-[11px] font-mono">{ext}</span>
              </div>
              <img
                src={previewURL}
                alt={fileName}
                loading="lazy"
                decoding="async"
                onError={(event) => {
                  event.currentTarget.style.display = "none";
                }}
                className="relative z-10 h-full w-full object-contain transition-transform duration-300 group-hover/img:scale-105"
              />
            </button>
          ) : (
            <div className="flex flex-col items-center gap-1 text-fg-subtle">
              <Film className="size-8" />
              <span className="text-xs font-mono font-medium">{ext} 文件</span>
            </div>
          )}

          <span className="pointer-events-none absolute top-2 right-2 z-20 rounded-md bg-black/75 px-1.5 py-0.5 text-[10px] font-mono text-white/90 backdrop-blur-md border border-white/10 shadow-xs">
            {ext} · {formatBytes(item.bytes)}
          </span>
        </div>
      }
    >
      <div className="space-y-1.5">
        <div className="truncate text-xs font-semibold text-fg tracking-tight" title={fileName}>
          {fileName}
        </div>

        <div className="truncate font-mono text-[11px] text-fg-muted flex items-center gap-1">
          <span className="truncate">{item.userName || "未识别用户"}</span>
          {item.userScreenName && (
            <span className="text-brand-500 font-medium shrink-0">@{item.userScreenName}</span>
          )}
        </div>

        <div className="flex items-center justify-between pt-2 border-t border-line/70 text-[11px] text-fg-subtle">
          <span className="font-mono tabular-nums text-[10px]">{formatDateTime(item.createdAt)}</span>
          <div className="flex items-center gap-1">
            <Tooltip title="复制本地路径">
              <Button
                size="sm"
                variant="text"
                circle
                icon={<Copy className="size-3" />}
                onClick={() => void copyToClipboard(item.filePath, "本地路径")}
                aria-label="复制本地路径"
              />
            </Tooltip>
            <Tooltip title="复制媒体直链">
              <Button
                size="sm"
                variant="text"
                circle
                icon={<ExternalLink className="size-3" />}
                onClick={() => void copyToClipboard(item.mediaUrl, "原始下载直链")}
                aria-label="复制下载直链"
              />
            </Tooltip>
          </div>
        </div>
      </div>
    </Card>
  );
});

export function GalleryPage({ filter: controlledFilter, onFilterChange }: GalleryPageProps) {
  const [localFilter, setLocalFilter] = useState<GalleryRouteState>(defaultGalleryFilter);
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
  const pageSize = galleryPageSize;
  // 输入框即时响应，查询键用 300ms 防抖后的值。
  const debouncedSearch = useDebouncedValue(filter.search, GALLERY_SEARCH_DEBOUNCE_MS);

  const libraryQuery = useQuery({
    queryKey: [
      ...libraryDownloadsQueryRoot,
      {
        page: filter.page,
        kind: filter.kind,
        user: filter.user,
        search: debouncedSearch,
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
        },
        signal,
      ),
    // 翻页/切筛选时保留上一页内容，避免整块骨架闪烁与分页条消失。
    placeholderData: keepPreviousData,
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
    if (!libraryQuery.data) return;
    if (filter.page > totalPages) {
      updateFilter({ page: totalPages }, { replace: true });
    }
  }, [libraryQuery.data, filter.page, totalPages, updateFilter]);

  const visibleEntries = indexedDownloads;
  const hasActiveFilter =
    filter.kind !== "all" || filter.user !== "all" || filter.search.trim() !== "";

  const previewIndex = useMemo(
    () =>
      previewId === null
        ? -1
        : visibleEntries.findIndex((entry) => entryKey(entry.item) === previewId),
    [previewId, visibleEntries],
  );
  const previewEntry = previewIndex >= 0 ? visibleEntries[previewIndex] : undefined;


  const openPreview = useCallback((nextId: string) => setPreviewId(nextId), []);

  // 跨页切图：走到当前页边界时翻一页，等新页数据落地后再把灯箱定位到首/末项。
  const [pendingPreviewEdge, setPendingPreviewEdge] = useState<"first" | "last" | null>(null);

  const stepPreview = useCallback(
    (delta: number) => {
      if (visibleEntries.length === 0 || previewIndex === -1) return;
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
    [filter.page, previewIndex, totalPages, updateFilter, visibleEntries],
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
    <div className="flex flex-col gap-4">
      {/* 顶部标题与筛选工具栏 */}
      <div className="flex flex-wrap items-center justify-between gap-4 pb-1">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-fg">媒体归档库</h1>
          <p className="mt-0.5 text-xs text-fg-muted">
            集中浏览所有已下载的高清图片、动态 GIF 与 4K 视频原片
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2.5">
          <Segmented
            size="md"
            value={filter.kind}
            onChange={(val) => updateFilter({ kind: val, page: 1 })}
            options={[
              { label: `全部 (${categoryCounts.all})`, value: "all" },
              { label: `图片 (${categoryCounts.images})`, value: "images" },
              { label: `视频 (${categoryCounts.videos})`, value: "videos" },
              { label: `动图 (${categoryCounts.gifs})`, value: "gifs" },
            ]}
          />

          <div className="w-56">
            <SearchSelect
              value={filter.user}
              onChange={(val) => updateFilter({ user: val, page: 1 })}
              options={[{ value: "all", label: "全部用户" }, ...userOptions]}
              placeholder="按用户筛选..."
            />
          </div>

          <SearchInput
            placeholder="搜索文件名、推文 ID..."
            value={filter.search}
            onChange={(e) => updateFilter({ search: e.target.value, page: 1 }, { replace: true })}
            allowClear
            className="w-52"
          />
        </div>
      </div>

      {libraryQuery.data?.truncated && (
        <Alert type="warning" message="媒体库索引已达上限，当前仅显示最近的记录" />
      )}

      {/* 媒体内容流展示 */}
      {libraryQuery.isLoading ? (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6">
          {Array.from({ length: 12 }, (_, index) => (
            <Card key={index} size="sm" bodyClassName="p-2 space-y-2">
              <Skeleton className="aspect-square w-full rounded" />
              <Skeleton className="h-3 w-4/5" />
              <Skeleton className="h-2.5 w-1/2" />
            </Card>
          ))}
        </div>
      ) : libraryQuery.isError && !libraryQuery.data ? (
        <Card className="py-8">
          <Alert
            type="error"
            showIcon
            message="媒体库加载失败"
            description={getErrorMessage(libraryQuery.error)}
            action={
              <Button
                size="sm"
                variant="default"
                loading={libraryQuery.isFetching}
                onClick={() => void libraryQuery.refetch()}
              >
                重试
              </Button>
            }
          />
        </Card>
      ) : visibleEntries.length === 0 ? (
        <Card className="text-center py-12">
          <Empty
            description={
              hasActiveFilter
                ? "没有匹配的媒体文件，试试调整筛选或搜索关键词。"
                : "暂无归档文件记录。完成下载任务后，媒体文件将自动在此呈现。"
            }
          />
        </Card>
      ) : (
        <div
          className={
            libraryQuery.isFetching
              ? "grid grid-cols-2 gap-3 opacity-60 transition-opacity sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6"
              : "grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6"
          }
        >
          {visibleEntries.map(({ item, isVideo, isImage, isGif }) => (
            <GalleryCard
              key={item.id || item.filePath}
              item={item}
              isVideo={isVideo}
              isPreviewableImage={isImage || isGif}
              onOpen={openPreview}
            />
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
            totalLabel={(total) => `共 ${total} 项`}
            onPageChange={goToPage}
          />
        </div>
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
            ? `${previewEntry.item.userName || "未识别用户"}${
                previewEntry.item.userScreenName ? `  @${previewEntry.item.userScreenName}` : ""
              }`
            : ""
        }
      >
        {previewEntry && (() => {
          const { item, isVideo } = previewEntry;
          const fileName = displayFileName(item);
          const previewURL = fileURL(item);

          return (
            <div className="flex flex-col">
              <div className="relative flex min-h-[50vh] max-h-[72vh] items-center justify-center bg-black/95 p-2 overflow-hidden">
                {isVideo ? (
                  <video
                    key={item.id}
                    src={previewURL}
                    controls
                    autoPlay
                    playsInline
                    className="max-h-[68vh] max-w-full rounded shadow-2xl"
                  />
                ) : (
                  <img
                    src={previewURL}
                    alt={fileName}
                    className="max-h-[68vh] max-w-full object-contain rounded shadow-2xl"
                  />
                )}

                {/* 左右切图浮动按钮（当前服务端分页内循环） */}
                <button
                  type="button"
                  aria-label="上一个媒体文件"
                  onClick={() => stepPreview(-1)}
                  className="absolute left-3 top-1/2 -translate-y-1/2 flex size-10 items-center justify-center rounded-full bg-black/60 text-white hover:bg-black/90 transition-colors cursor-pointer"
                >
                  <ChevronLeft className="size-5" />
                </button>
                <button
                  type="button"
                  aria-label="下一个媒体文件"
                  onClick={() => stepPreview(1)}
                  className="absolute right-3 top-1/2 -translate-y-1/2 flex size-10 items-center justify-center rounded-full bg-black/60 text-white hover:bg-black/90 transition-colors cursor-pointer"
                >
                  <ChevronRight className="size-5" />
                </button>
              </div>

              {/* 弹窗底部操作与详情栏 */}
              <div className="flex flex-wrap items-center justify-between gap-3 border-t border-line bg-surface px-4 py-3 text-xs">
                <div className="min-w-0 max-w-md">
                  <div className="font-medium text-fg truncate" title={fileName}>
                    {fileName}
                  </div>
                  <div className="font-mono text-fg-muted text-[11px] truncate">
                    {formatBytes(item.bytes)} · {formatDateTime(item.createdAt)}
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    variant="default"
                    icon={<Copy className="size-3.5" />}
                    onClick={() => void copyToClipboard(item.filePath, "本地路径")}
                  >
                    复制本地路径
                  </Button>
                  <Button
                    size="sm"
                    variant="default"
                    icon={<ExternalLink className="size-3.5" />}
                    onClick={() => void copyToClipboard(item.mediaUrl, "原始下载直链")}
                  >
                    复制直链
                  </Button>
                </div>
              </div>
            </div>
          );
        })()}
      </MediaModal>
    </div>
  );
}
