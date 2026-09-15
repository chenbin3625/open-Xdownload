import {
  ChevronLeft,
  ChevronRight,
  Copy,
  ExternalLink,
  Film,
  Image as ImageIcon,
  PictureInPicture,
  RefreshCw,
  Video,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, {
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  formatBytes,
  getLibraryDownloads,
  getPosterBackfillStatus,
  libraryDownloadsLimit,
  libraryDownloadsQueryRoot,
  posterBackfillQueryRoot,
  startPosterBackfill,
  type DownloadRecord,
  type Job,
} from "../lib/api";
import { copyToClipboard, formatDateTime } from "../components/common/CommonUI";
import { Button } from "../components/ui/Button";
import { Card } from "../components/ui/Card";
import { Segmented } from "../components/ui/Controls";
import { Empty, Skeleton } from "../components/ui/Feedback";
import { SearchInput } from "../components/ui/Input";
import { MediaModal, Tooltip } from "../components/ui/Overlay";
import { Pagination } from "../components/ui/Pagination";
import { SearchSelect } from "../components/ui/SearchSelect";
import { Spinner } from "../components/ui/Spinner";
import { toast } from "../components/ui/Toast";

const VIDEO_EXTENSIONS = [".mp4", ".mov", ".m4v", ".webm", ".ogv"];
const IMAGE_EXTENSIONS = [".jpg", ".jpeg", ".png", ".webp"];

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

export interface GalleryPageProps {
  jobs?: Job[];
  downloads?: DownloadRecord[];
}

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
        <div className="flex size-10 items-center justify-center rounded-full bg-brand-500/90 text-white shadow-md">
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
            <VideoPoster item={item} fileName={fileName} onOpen={() => onOpen(entryKey(item))} />
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

export function GalleryPage({ downloads }: GalleryPageProps) {
  const [filterType, setFilterType] = useState<string>("all");
  const [searchFilter, setSearchFilter] = useState<string>("");
  const [userFilter, setUserFilter] = useState<string>("all");
  const [previewId, setPreviewId] = useState<string | null>(null);
  const [currentPage, setCurrentPage] = useState(1);
  const pageSize = 60;
  const deferredSearchFilter = useDeferredValue(searchFilter);

  const libraryQuery = useQuery({
    queryKey: libraryDownloadsQueryRoot,
    queryFn: ({ signal }) => getLibraryDownloads(libraryDownloadsLimit, signal),
    staleTime: 10 * 60_000,
    gcTime: 30 * 60_000,
    refetchOnWindowFocus: false,
    enabled: !downloads,
  });

  const queryClient = useQueryClient();
  const backfillQuery = useQuery({
    queryKey: posterBackfillQueryRoot,
    queryFn: ({ signal }) => getPosterBackfillStatus(signal),
    refetchInterval: (query) => (query.state.data?.running ? 1500 : false),
  });
  const backfillStatus = backfillQuery.data;
  const backfillRunning = backfillStatus?.running ?? false;

  const backfillMutation = useMutation({
    mutationFn: startPosterBackfill,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: posterBackfillQueryRoot });
    },
    onError: (error) => {
      toast.error({ message: "封面补齐启动失败", description: String(error) });
    },
  });

  const backfillWasRunning = useRef(false);
  useEffect(() => {
    if (!backfillStatus) return;
    const wasRunning = backfillWasRunning.current;
    backfillWasRunning.current = backfillStatus.running;
    if (wasRunning && !backfillStatus.running && backfillStatus.total > 0) {
      if (backfillStatus.failed > 0) {
        toast.info({
          message: "封面补齐完成",
          description: `新增 ${backfillStatus.fetched} 张，跳过 ${backfillStatus.skipped} 张，失败 ${backfillStatus.failed} 张`,
        });
      } else {
        toast.success({
          message: "封面补齐完成",
          description: `新增 ${backfillStatus.fetched} 张，其余均已存在`,
        });
      }
      void queryClient.invalidateQueries({ queryKey: libraryDownloadsQueryRoot });
    }
  }, [backfillStatus, queryClient]);

  const allDownloads = downloads ?? libraryQuery.data ?? [];

  const indexedDownloads = useMemo(
    () =>
      allDownloads.map((item) => {
        const lower = item.filePath.toLowerCase().split("?")[0];
        return {
          item,
          isVideo: VIDEO_EXTENSIONS.some((ext) => lower.endsWith(ext)),
          isImage: IMAGE_EXTENSIONS.some((ext) => lower.endsWith(ext)),
          isGif: lower.endsWith(".gif"),
        };
      }),
    [allDownloads],
  );

  const categoryCounts = useMemo(() => {
    const counts = { all: allDownloads.length, images: 0, videos: 0, gifs: 0 };
    for (const entry of indexedDownloads) {
      if (entry.isVideo) counts.videos += 1;
      if (entry.isImage) counts.images += 1;
      if (entry.isGif) counts.gifs += 1;
    }
    return counts;
  }, [allDownloads.length, indexedDownloads]);

  const userOptions = useMemo(() => {
    const users = new Map<string, string>();
    for (const item of allDownloads) {
      const key = item.userScreenName || "unknown";
      const label = item.userScreenName
        ? `${item.userName || item.userScreenName} (@${item.userScreenName})`
        : "未识别用户";
      users.set(key, label);
    }
    return [...users.entries()]
      .sort((a, b) => a[1].localeCompare(b[1], "zh-CN"))
      .map(([value, label]) => ({ value, label }));
  }, [allDownloads]);

  const filteredEntries = useMemo(() => {
    const kw = deferredSearchFilter.trim().toLowerCase();
    return indexedDownloads.filter((entry) => {
      const { item } = entry;
      if (filterType === "images" && !entry.isImage) return false;
      if (filterType === "videos" && !entry.isVideo) return false;
      if (filterType === "gifs" && !entry.isGif) return false;

      if (userFilter !== "all" && (item.userScreenName || "unknown") !== userFilter) {
        return false;
      }

      if (kw) {
        return (
          item.filePath.toLowerCase().includes(kw) ||
          item.mediaUrl.toLowerCase().includes(kw) ||
          String(item.tweetId).includes(kw)
        );
      }

      return true;
    });
  }, [deferredSearchFilter, filterType, indexedDownloads, userFilter]);

  useEffect(() => {
    setCurrentPage(1);
  }, [filterType, searchFilter, userFilter]);

  const visibleEntries = useMemo(
    () => filteredEntries.slice((currentPage - 1) * pageSize, currentPage * pageSize),
    [currentPage, filteredEntries],
  );

  const previewIndex = useMemo(
    () =>
      previewId === null
        ? -1
        : filteredEntries.findIndex((entry) => entryKey(entry.item) === previewId),
    [filteredEntries, previewId],
  );
  const previewEntry = previewIndex >= 0 ? filteredEntries[previewIndex] : undefined;

  useEffect(() => {
    if (previewId !== null && previewIndex === -1) {
      setPreviewId(null);
      return;
    }
    if (previewIndex === -1) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        event.preventDefault();
        if (filteredEntries.length === 0) return;
        const delta = event.key === "ArrowRight" ? 1 : -1;
        const nextIndex =
          (previewIndex + delta + filteredEntries.length) % filteredEntries.length;
        setPreviewId(entryKey(filteredEntries[nextIndex].item));
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [filteredEntries, previewId, previewIndex]);

  const openPreview = useCallback((nextId: string) => setPreviewId(nextId), []);
  const stepPreview = useCallback(
    (delta: number) => {
      if (filteredEntries.length === 0 || previewIndex === -1) return;
      const nextIndex =
        (previewIndex + delta + filteredEntries.length) % filteredEntries.length;
      setPreviewId(entryKey(filteredEntries[nextIndex].item));
    },
    [filteredEntries, previewIndex],
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
            value={filterType}
            onChange={(val) => setFilterType(val)}
            options={[
              { label: `全部 (${categoryCounts.all})`, value: "all" },
              { label: `图片 (${categoryCounts.images})`, value: "images" },
              { label: `视频 (${categoryCounts.videos})`, value: "videos" },
              { label: `动图 (${categoryCounts.gifs})`, value: "gifs" },
            ]}
          />

          <div className="w-56">
            <SearchSelect
              value={userFilter}
              onChange={setUserFilter}
              options={[{ value: "all", label: "全部用户" }, ...userOptions]}
              placeholder="按用户筛选..."
            />
          </div>

          <SearchInput
            placeholder="搜索文件名、推文 ID..."
            value={searchFilter}
            onChange={(e) => setSearchFilter(e.target.value)}
            allowClear
            className="w-52"
          />

          <Tooltip title="扫描媒体库，为缺失封面的视频/GIF 重新拉取预览图并保存到本地；已有封面的记录会自动跳过">
            <Button
              variant="default"
              icon={<RefreshCw className={`size-3.5 ${backfillRunning ? "animate-spin" : ""}`} />}
              loading={backfillMutation.isPending}
              disabled={backfillRunning}
              onClick={() => backfillMutation.mutate()}
            >
              {backfillRunning && backfillStatus
                ? `补齐封面 ${backfillStatus.done}/${backfillStatus.total}`
                : "补齐视频封面"}
            </Button>
          </Tooltip>
        </div>
      </div>

      {/* 媒体内容流展示 */}
      {!downloads && libraryQuery.isLoading ? (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6">
          {Array.from({ length: 12 }, (_, index) => (
            <Card key={index} size="sm" bodyClassName="p-2 space-y-2">
              <Skeleton className="aspect-square w-full rounded" />
              <Skeleton className="h-3 w-4/5" />
              <Skeleton className="h-2.5 w-1/2" />
            </Card>
          ))}
        </div>
      ) : filteredEntries.length === 0 ? (
        <Card className="text-center py-12">
          <Empty description="暂无归档文件记录。完成下载任务后，媒体文件将自动在此呈现。" />
        </Card>
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
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

      {/* 底部分页 */}
      {filteredEntries.length > pageSize && (
        <div className="flex justify-center pt-3">
          <Pagination
            page={currentPage}
            pageSize={pageSize}
            total={filteredEntries.length}
            totalLabel={(total) => `共 ${total} 项`}
            onPageChange={(page) => setCurrentPage(page)}
          />
        </div>
      )}

      {/* 沉浸式多媒体灯箱预览弹窗 */}
      <MediaModal
        open={previewEntry !== undefined}
        onClose={() => setPreviewId(null)}
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

                {/* 左右切图浮动按钮 */}
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
