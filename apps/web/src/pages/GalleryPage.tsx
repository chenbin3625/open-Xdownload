import { t, useTranslation } from "../lib/i18n";
import {
  Archive,
  CalendarDays,
  ChevronLeft,
  ChevronRight,
  Copy,
  Download,
  ExternalLink,
  Film,
  FolderPlus,
  HardDrive,
  LayoutGrid,
  Link,
  List,
  Image as ImageIcon,
  PictureInPicture,
  Search,
  Star,
  Tag as TagIcon,
  Trash2,
  Users,
  Video,
  X,
} from "lucide-react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Card from "@douyinfe/semi-ui/lib/es/card";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import InputNumber from "@douyinfe/semi-ui/lib/es/inputNumber";
import RadioGroup from "@douyinfe/semi-ui/lib/es/radio/radioGroup";
import Select from "@douyinfe/semi-ui/lib/es/select";
import {
  addCollectionItems,
  batchDeleteDownloads,
  collectionsQueryRoot,
  configQueryRoot,
  deleteDownload,
  exportMediaZip,
  formatBytes,
  getCollections,
  getLibraryDownloadsPage,
  getLibraryUsers,
  getTags,
  libraryDownloadsQueryRoot,
  libraryUsersQueryRoot,
  setDownloadStar,
  setDownloadTags,
  tagsQueryRoot,
  type AppConfig,
  type DownloadRecord,
  type LibraryUserSummary,
} from "../lib/api";
import { copyToClipboard, formatDateTime, getErrorMessage, notifyError } from "../components/common/CommonUI";
import { smallTwimgUrl } from "../lib/tweetDisplay";
import { useDebouncedValue } from "../lib/useDebouncedValue";
import {
  defaultGalleryFilter,
  galleryPageSize,
  type GalleryRouteState,
  type GallerySort,
} from "../lib/useRouteState";
import { Alert, Empty, Skeleton } from "../components/semi/feedback";
import { MediaModal, Modal, SimplePopover, Tooltip } from "../components/semi/overlays";
import { Pagination, Tag } from "../components/semi/data";
import { Checkbox as ControlCheckbox, Field, SearchSelect, Select as ControlSelect } from "../components/semi/controls";
import { toast } from "../components/semi/toast";
import { OrganizeManagerModal, safeCollectionList, safeTagList } from "../components/organize/OrganizeManagerModal";

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

/** 搜索与体积输入防抖：避免每敲一个字符就打一次服务端分页查询。 */
const GALLERY_SEARCH_DEBOUNCE_MS = 300;
/** 按账号卡片视图的每页条数：避免数千归档账号一次性渲染造成主线程卡顿。 */
const LIBRARY_USERS_PAGE_SIZE = 48;
/** 导出清单的 Blob URL 延迟释放，避免浏览器尚未开始读取下载流就被提前 revoke。 */
const OBJECT_URL_REVOKE_DELAY_MS = 60_000;
/** 批量下载逐个错峰触发，避免同一 tick 内连续触发数十次 <a>.click() 被浏览器限流丢弃。 */
const BATCH_DOWNLOAD_STAGGER_MS = 150;

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
  offlineThumbnails = false,
  onOpen,
  onToggleStar,
  onRequestDelete,
}: {
  item: DownloadRecord;
  isVideo: boolean;
  isPreviewableImage: boolean;
  thumbnailFit: "contain" | "cover";
  offlineThumbnails?: boolean;
  onOpen: (previewId: string) => void;
  onToggleStar?: (item: DownloadRecord) => void;
  onRequestDelete?: (item: DownloadRecord) => void;
}) {
  useTranslation();
  const fileName = displayFileName(item);
  const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
  const previewURL = fileURL(item);
  // 缩略图默认优先图床小图，开启离线缩略图时优先走本地缩略图缓存；下载链接仍指向原文件。
  const remoteThumbnail = useMemo(() => (offlineThumbnails ? undefined : smallTwimgUrl(item.previewUrl)), [offlineThumbnails, item.previewUrl]);
  const offlineThumbUrl = offlineThumbnails && item.id > 0 ? `/api/library/downloads/${item.id}/thumbnail` : undefined;
  const localThumbnail = item.id > 0 ? `/api/library/downloads/${item.id}/file` : previewURL;
  const preferredSource = offlineThumbUrl ?? remoteThumbnail ?? localThumbnail;
  const [imageSource, setImageSource] = useState(preferredSource);
  const [imageFailed, setImageFailed] = useState(false);
  useEffect(() => {
    setImageFailed(false);
    setImageSource(preferredSource);
  }, [preferredSource]);

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
                    referrerPolicy={imageSource === remoteThumbnail ? "no-referrer" : undefined}
                    onError={() => { if (imageSource !== localThumbnail) setImageSource(localThumbnail); else setImageFailed(true); }}
                    className={`relative z-10 h-full w-full ${thumbnailFit === "cover" ? "object-cover" : "object-contain"} transition-transform duration-200 group-hover/img:scale-[1.03]`}
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

        {item.tags && item.tags.length > 0 && (
          <div className="mt-1 flex flex-wrap gap-1">
            {item.tags.map((tg) => (
              <Tag key={tg.id} size="sm">{tg.name}</Tag>
            ))}
          </div>
        )}

        <div className="gallery-card-meta">
          {item.tweetId && <a className="text-brand-600" href={`/tweets?id=${encodeURIComponent(item.tweetId)}`}>{t("archive.title")}</a>}
          <time dateTime={item.createdAt} title={formatDateTime(item.createdAt)}>{formatDateTime(item.createdAt)}</time>
          <div className="gallery-card-actions">
            {item.id > 0 && onToggleStar && (
              <Tooltip title={item.starred ? t("取消收藏") : t("收藏媒体")}>
                <Button
                  theme="borderless"
                  type={item.starred ? "warning" : "tertiary"}
                  icon={<Star size={16} fill={item.starred ? "currentColor" : "none"} />}
                  onClick={() => onToggleStar(item)}
                  aria-label={item.starred ? t("取消收藏") : t("收藏媒体")}
                  aria-pressed={Boolean(item.starred)}
                />
              </Tooltip>
            )}
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
            {item.id > 0 && onRequestDelete && (
              <Tooltip title={t("删除媒体")}>
                <Button
                  theme="borderless"
                  type="danger"
                  icon={<Trash2 size={16} />}
                  onClick={() => onRequestDelete(item)}
                  aria-label={t("删除媒体")}
                />
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
  const avatarCandidates = useMemo(() => {
    const list: { url: string; remote: boolean }[] = [];
    if (summary.screenName && summary.avatarUrl) {
      list.push({ url: `/api/avatars/${encodeURIComponent(summary.screenName)}`, remote: false });
    }
    if (summary.avatarUrl) {
      list.push({ url: summary.avatarUrl, remote: true });
    }
    return list;
  }, [summary.screenName, summary.avatarUrl]);
  const [avatarIdx, setAvatarIdx] = useState(0);
  useEffect(() => { setAvatarIdx(0); }, [summary.screenName, summary.avatarUrl]);
  return (
    <Card shadows="hover" className="gallery-card min-w-0" bodyStyle={{ padding: 12 }}>
      <button
        type="button"
        onClick={() => onOpen(summary.screenName)}
        className="flex w-full cursor-pointer items-center gap-3 border-0 bg-transparent p-0 text-left"
        aria-label={t("查看 @{{value0}} 的媒体", { value0: summary.screenName })}
      >
        {avatarIdx < avatarCandidates.length ? (
          <img
            src={avatarCandidates[avatarIdx].url}
            alt=""
            loading="lazy"
            decoding="async"
            referrerPolicy={avatarCandidates[avatarIdx].remote ? "no-referrer" : undefined}
            onError={() => setAvatarIdx((i) => i + 1)}
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
  const queryClient = useQueryClient();
  const [localFilter, setLocalFilter] = useState<GalleryRouteState>(defaultGalleryFilter);
  // 归档库的两种浏览方式：媒体网格 / 按账号卡片。
  const [viewMode, setViewMode] = useState<"media" | "users">("media");
  const [usersPage, setUsersPage] = useState(1);
  const [usersSearch, setUsersSearch] = useState("");
  const debouncedUsersSearch = useDebouncedValue(usersSearch, GALLERY_SEARCH_DEBOUNCE_MS);
  const [cachedUserOptions, setCachedUserOptions] = useState<{ value: string; label: string }[]>([]);
  const filter = controlledFilter ?? localFilter;
  const offlineThumbnails = Boolean(queryClient.getQueryData<AppConfig>(configQueryRoot)?.offlineThumbnails);
  const [organizeOpen, setOrganizeOpen] = useState(false);
  const [organizeLoaded, setOrganizeLoaded] = useState(false);
  const [deletingIds, setDeletingIds] = useState<number[] | null>(null);
  const [deleteLocalFiles, setDeleteLocalFiles] = useState(false);
  const [organizingDownload, setOrganizingDownload] = useState<DownloadRecord | null>(null);
  const [batchCollectionOpen, setBatchCollectionOpen] = useState(false);
  const [selectedTagIds, setSelectedTagIds] = useState<number[]>([]);
  const [selectedCollectionId, setSelectedCollectionId] = useState("");
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
  const [previewZoomed, setPreviewZoomed] = useState(false);
  const [view, setView] = useState(() => { try { return localStorage.getItem("gallery-view") === "list" ? "list" : "grid"; } catch { return "grid"; } });
  const [density, setDensity] = useState(() => { try { return localStorage.getItem("gallery-density") === "compact" ? "compact" : "standard"; } catch { return "standard"; } });
  const [thumbnailFit, setThumbnailFit] = useState<"contain" | "cover">(() => { try { return localStorage.getItem("gallery-thumbnail-fit") === "cover" ? "cover" : "contain"; } catch { return "contain"; } });
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const lastSelectedKeyRef = useRef<string | null>(null);
  const shiftSelectRef = useRef(false);
  useEffect(() => { setPreviewZoomed(false); }, [previewId]);
  useEffect(() => { try { localStorage.setItem("gallery-view", view); localStorage.setItem("gallery-density", density); localStorage.setItem("gallery-thumbnail-fit", thumbnailFit); } catch { /* Storage may be unavailable in private browsing. */ } }, [view, density, thumbnailFit]);
  useEffect(() => { setSelected(new Set()); lastSelectedKeyRef.current = null; }, [filter.page, filter.kind, filter.user, filter.search, filter.sort, filter.from, filter.to, filter.minBytes, filter.maxBytes, filter.starred, filter.tagId, filter.collectionId]);
  const pageSize = galleryPageSize;
  // 输入框即时响应，查询键用 300ms 防抖后的值。
  const debouncedSearch = useDebouncedValue(filter.search, GALLERY_SEARCH_DEBOUNCE_MS);
  const debouncedMinBytes = useDebouncedValue(filter.minBytes, GALLERY_SEARCH_DEBOUNCE_MS);
  const debouncedMaxBytes = useDebouncedValue(filter.maxBytes, GALLERY_SEARCH_DEBOUNCE_MS);
  const filtersPendingDebounce =
    debouncedSearch !== filter.search ||
    debouncedMinBytes !== filter.minBytes ||
    debouncedMaxBytes !== filter.maxBytes;
  const rangeError = filter.from && filter.to && filter.from > filter.to
    ? t("开始日期不能晚于结束日期")
    : filter.minBytes !== "" && filter.maxBytes !== "" && Number(filter.minBytes) > Number(filter.maxBytes)
      ? t("最小体积不能大于最大体积") : "";

  const shouldLoadOrganize = organizeLoaded || organizeOpen || Boolean(organizingDownload) || batchCollectionOpen || Boolean(filter.tagId) || Boolean(filter.collectionId);
  const tagsQuery = useQuery({
    queryKey: tagsQueryRoot,
    queryFn: ({ signal }) => getTags(signal),
    enabled: shouldLoadOrganize,
    staleTime: 60_000,
  });
  const collectionsQuery = useQuery({
    queryKey: collectionsQueryRoot,
    queryFn: ({ signal }) => getCollections(signal),
    enabled: shouldLoadOrganize,
    staleTime: 60_000,
  });
  const tagsList = safeTagList(tagsQuery.data);
  const collectionsList = safeCollectionList(collectionsQuery.data);

  // 只有切到「按账号」时才拉分页聚合数据，避免每次进归档库或展开数千张卡片卡顿。
  const trimmedUsersSearch = debouncedUsersSearch.trim();
  const usersQuery = useQuery({
    queryKey: [...libraryUsersQueryRoot, { page: usersPage, pageSize: LIBRARY_USERS_PAGE_SIZE, search: trimmedUsersSearch }],
    queryFn: ({ signal }) =>
      getLibraryUsers({
        page: usersPage,
        pageSize: LIBRARY_USERS_PAGE_SIZE,
        search: trimmedUsersSearch || undefined,
        signal,
      }),
    placeholderData: keepPreviousData,
    enabled: viewMode === "users",
    staleTime: 30_000,
  });

  const isDefaultUnfilteredFirstPage =
    filter.page === 1 &&
    filter.kind === "all" &&
    filter.user === "all" &&
    debouncedSearch === "" &&
    filter.sort === "date_desc" &&
    filter.from === "" &&
    filter.to === "" &&
    debouncedMinBytes === "" &&
    debouncedMaxBytes === "" &&
    !filter.starred &&
    !filter.tagId &&
    !filter.collectionId;
  const shouldOmitUsers = cachedUserOptions.length > 0 && !isDefaultUnfilteredFirstPage;

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
        minBytes: debouncedMinBytes,
        maxBytes: debouncedMaxBytes,
        starred: filter.starred ?? "",
        tagId: filter.tagId ?? "",
        collectionId: filter.collectionId ?? "",
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
          minBytes: debouncedMinBytes,
          maxBytes: debouncedMaxBytes,
          starred: filter.starred || undefined,
          tagId: filter.tagId || undefined,
          collectionId: filter.collectionId || undefined,
          omitUsers: shouldOmitUsers,
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

  useEffect(() => {
    const incomingUsers = libraryQuery.data?.users;
    if (incomingUsers && incomingUsers.length > 0) {
      setCachedUserOptions(incomingUsers);
    }
  }, [libraryQuery.data?.users]);

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
  const userOptions = (libraryQuery.data?.users?.length ?? 0) > 0 ? libraryQuery.data!.users : cachedUserOptions;
  const allUsersLabel = t("全部用户");
  const userSelectOptions = useMemo(
    () => [{ value: "all", label: allUsersLabel }, ...userOptions],
    [allUsersLabel, userOptions],
  );
  const handleUserFilterChange = useCallback(
    (val: string) => updateFilter({ user: val, page: 1 }),
    [updateFilter],
  );
  const totalEntries = libraryQuery.data?.pagination.total ?? 0;
  const totalPages = Math.max(
    1,
    libraryQuery.data?.pagination.totalPages ?? Math.ceil(totalEntries / pageSize),
  );
  const usersItems = usersQuery.data?.items ?? [];
  const usersTotal = usersQuery.data?.pagination?.total ?? usersItems.length;
  const usersTotalPages = Math.max(
    1,
    usersQuery.data?.pagination?.totalPages ?? Math.ceil(usersTotal / LIBRARY_USERS_PAGE_SIZE),
  );
  useEffect(() => {
    if (!usersQuery.data || usersQuery.isPlaceholderData || usersSearch !== debouncedUsersSearch) return;
    if (usersPage > usersTotalPages) {
      setUsersPage(usersTotalPages);
    }
  }, [usersQuery.data, usersQuery.isPlaceholderData, usersSearch, debouncedUsersSearch, usersPage, usersTotalPages]);

  // 数据变少（删除/筛选后总数下降）时把页码收敛到最后一页，避免停在空页。
  useEffect(() => {
    if (!libraryQuery.data || libraryQuery.isPlaceholderData || filtersPendingDebounce) return;
    if (filter.page > totalPages) {
      updateFilter({ page: totalPages }, { replace: true });
    }
  }, [libraryQuery.data, libraryQuery.isPlaceholderData, filtersPendingDebounce, filter.page, totalPages, updateFilter]);

  const visibleEntries = indexedDownloads;
  const selectedEntries = useMemo(
    () => visibleEntries.filter(({ item }) => selected.has(entryKey(item))),
    [selected, visibleEntries],
  );
  const selectionDisabled = libraryQuery.isPlaceholderData || Boolean(rangeError) || filtersPendingDebounce;
  const hasActiveFilter =
    filter.kind !== "all" || filter.user !== "all" || filter.search.trim() !== "" ||
    filter.from !== "" || filter.to !== "" || filter.minBytes !== "" || filter.maxBytes !== "" ||
    Boolean(filter.starred) || Boolean(filter.tagId) || Boolean(filter.collectionId);

  const starMutation = useMutation({
    mutationFn: ({ id, starred }: { id: number; starred: boolean }) => setDownloadStar(id, starred),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: libraryDownloadsQueryRoot }); },
    onError: notifyError(t("操作失败")),
  });

  const deleteMutation = useMutation({
    mutationFn: async ({ ids, removeFiles }: { ids: number[]; removeFiles: boolean }) =>
      ids.length === 1 ? deleteDownload(ids[0], removeFiles) : batchDeleteDownloads(ids, removeFiles),
    onSuccess: (res) => {
      setDeletingIds(null);
      setDeleteLocalFiles(false);
      setSelected(new Set());
      setPreviewId(null);
      setCachedUserOptions([]);
      void queryClient.invalidateQueries({ queryKey: libraryDownloadsQueryRoot });
      void queryClient.invalidateQueries({ queryKey: libraryUsersQueryRoot });
      toast.success({
        message: t("已删除 {{value0}} 条媒体记录", { value0: res.deletedRecords }),
        description: res.deletedFiles > 0 ? t("同时移除了 {{value0}} 个本地文件", { value0: res.deletedFiles }) : undefined,
      });
    },
    onError: notifyError(t("删除失败")),
  });

  const saveOrganizeDownload = useMutation({
    mutationFn: async () => {
      if (!organizingDownload) return;
      await setDownloadTags(organizingDownload.id, selectedTagIds);
      if (selectedCollectionId) {
        await addCollectionItems(Number(selectedCollectionId), "download", [String(organizingDownload.id)]);
      }
    },
    onSuccess: () => {
      setOrganizingDownload(null);
      setSelectedCollectionId("");
      void queryClient.invalidateQueries({ queryKey: libraryDownloadsQueryRoot });
      void queryClient.invalidateQueries({ queryKey: collectionsQueryRoot });
      toast.success({ message: t("已更新标签与合集") });
    },
    onError: notifyError(t("保存失败")),
  });

  const batchAddCollection = useMutation({
    mutationFn: async () => {
      const ids = selectedEntries.map(({ item }) => item.id).filter((id) => id > 0).map(String);
      if (!selectedCollectionId || ids.length === 0) return;
      await addCollectionItems(Number(selectedCollectionId), "download", ids);
    },
    onSuccess: () => {
      setBatchCollectionOpen(false);
      setSelectedCollectionId("");
      void queryClient.invalidateQueries({ queryKey: collectionsQueryRoot });
      toast.success({ message: t("已加入合集") });
    },
    onError: notifyError(t("操作失败")),
  });

  const zipExportMutation = useMutation({
    mutationFn: async () => {
      const ids = selectedEntries.map(({ item }) => item.id).filter((id) => id > 0);
      if (ids.length === 0) throw new Error(t("所选文件尚未登记，无法打包"));
      return exportMediaZip({ ids });
    },
    onSuccess: (blob) => {
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `open-xdownload-media-${new Date().toISOString().slice(0, 10)}.zip`;
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), OBJECT_URL_REVOKE_DELAY_MS);
    },
    onError: notifyError(t("打包下载失败")),
  });

  const handleToggleDownloadStar = useCallback((item: DownloadRecord) => {
    if (item.id <= 0) return;
    starMutation.mutate({ id: item.id, starred: !item.starred });
  }, [starMutation.mutate]);

  const handleRequestDeleteDownload = useCallback((item: DownloadRecord) => {
    if (item.id <= 0) return;
    setDeleteLocalFiles(false);
    setDeletingIds([item.id]);
  }, []);

  const downloadTimersRef = useRef<number[]>([]);
  useEffect(() => () => {
    downloadTimersRef.current.forEach((timerId) => window.clearTimeout(timerId));
    downloadTimersRef.current = [];
  }, []);

  const downloadSelected = useCallback(() => {
    downloadTimersRef.current.forEach((timerId) => window.clearTimeout(timerId));
    downloadTimersRef.current = [];
    const triggerDownload = (item: DownloadRecord) => {
      const anchor = document.createElement("a");
      anchor.href = fileURL(item);
      anchor.download = displayFileName(item);
      anchor.rel = "noreferrer";
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
    };
    selectedEntries.forEach(({ item }, index) => {
      if (index === 0) {
        triggerDownload(item);
        return;
      }
      const timerId = window.setTimeout(() => {
        downloadTimersRef.current = downloadTimersRef.current.filter((id) => id !== timerId);
        triggerDownload(item);
      }, index * BATCH_DOWNLOAD_STAGGER_MS);
      downloadTimersRef.current.push(timerId);
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
    window.setTimeout(() => URL.revokeObjectURL(url), OBJECT_URL_REVOKE_DELAY_MS);
  }, [selectedEntries]);

  // 跨页切图：走到当前页边界时翻一页，等新页数据落地后再把灯箱定位到首/末项。
  const [pendingPreviewEdge, setPendingPreviewEdge] = useState<"first" | "last" | null>(null);
  const lastPreviewEntryRef = useRef<(typeof visibleEntries)[number] | undefined>(undefined);

  const previewIndex = useMemo(() => {
    if (previewId === null) return -1;
    const direct = visibleEntries.findIndex((entry) => entryKey(entry.item) === previewId);
    if (direct >= 0) return direct;
    // 新页刚落地但 effect 尚未提交新 previewId 时，渲染期直接解析首/末项，避免灯箱闪烁卸载。
    if (pendingPreviewEdge && !libraryQuery.isError && !libraryQuery.isPlaceholderData && visibleEntries.length > 0) {
      return pendingPreviewEdge === "first" ? 0 : visibleEntries.length - 1;
    }
    return -1;
  }, [previewId, visibleEntries, pendingPreviewEdge, libraryQuery.isError, libraryQuery.isPlaceholderData]);

  const previewEntry =
    previewIndex >= 0
      ? visibleEntries[previewIndex]
      : pendingPreviewEdge && !libraryQuery.isError
        ? lastPreviewEntryRef.current
        : undefined;

  if (previewIndex >= 0) {
    lastPreviewEntryRef.current = visibleEntries[previewIndex];
  } else if (previewId === null || libraryQuery.isError) {
    lastPreviewEntryRef.current = undefined;
  }

  const openPreview = useCallback((nextId: string) => setPreviewId(nextId), []);
  const openUserMedia = useCallback((screenName: string) => {
    setViewMode("media");
    updateFilter({ user: screenName, page: 1 });
  }, [updateFilter]);

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
  }, [pendingPreviewEdge, libraryQuery.data, libraryQuery.isPlaceholderData, libraryQuery.isError, visibleEntries]);

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
      setPendingPreviewEdge(null);
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
            <Button type="tertiary" icon={<FolderPlus size={16} />} onClick={() => { setOrganizeLoaded(true); setOrganizeOpen(true); }}>{t("标签与合集")}</Button>
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
            onChange={handleUserFilterChange}
            options={userSelectOptions}
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
        <Tooltip title={t("仅显示已收藏")}>
          <Button
            icon={<Star size={16} fill={filter.starred === "1" ? "currentColor" : "none"} />}
            theme="light"
            type={filter.starred === "1" ? "warning" : "tertiary"}
            aria-label={t("仅显示已收藏")}
            aria-pressed={filter.starred === "1"}
            onClick={() => updateFilter({ starred: filter.starred === "1" ? "" : "1", page: 1 })}
          />
        </Tooltip>
        <SimplePopover trigger={<Button
          icon={<TagIcon size={16} />}
          theme="light"
          type={filter.tagId || filter.collectionId ? "primary" : "tertiary"}
          aria-label={t("标签与合集筛选")}
          aria-pressed={Boolean(filter.tagId || filter.collectionId)}
          onClick={() => setOrganizeLoaded(true)}
        />}>
          <div className="space-y-3 min-w-56">
            <Field label={t("按标签筛选")}>
              <ControlSelect
                ariaLabel={t("按标签筛选")}
                value={filter.tagId ?? ""}
                onChange={(val) => updateFilter({ tagId: val, page: 1 })}
                options={[{ value: "", label: t("全部标签") }, ...tagsList.map((tg) => ({ value: String(tg.id), label: tg.name }))]}
              />
            </Field>
            <Field label={t("按合集筛选")}>
              <ControlSelect
                ariaLabel={t("按合集筛选")}
                value={filter.collectionId ?? ""}
                onChange={(val) => updateFilter({ collectionId: val, page: 1 })}
                options={[{ value: "", label: t("全部合集") }, ...collectionsList.map((col) => ({ value: String(col.id), label: col.name }))]}
              />
            </Field>
          </div>
        </SimplePopover>
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
        {hasActiveFilter && <Tooltip title={t("清除筛选")}><Button theme="borderless" type="tertiary" icon={<X size={16} />} aria-label={t("清除筛选")} onClick={() => updateFilter({ kind: "all", user: "all", search: "", from: "", to: "", minBytes: "", maxBytes: "", ...(filter.starred ? { starred: "" } : {}), ...(filter.tagId ? { tagId: "" } : {}), ...(filter.collectionId ? { collectionId: "" } : {}), page: 1 })} /></Tooltip>}
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
          { key: "starred", label: filter.starred === "1" ? t("已收藏") : "", patch: { starred: "" } },
          { key: "tagId", label: filter.tagId ? `${t("标签")}: ${tagsList.find((tg) => String(tg.id) === filter.tagId)?.name ?? filter.tagId}` : "", patch: { tagId: "" } },
          { key: "collectionId", label: filter.collectionId ? `${t("合集")}: ${collectionsList.find((col) => String(col.id) === filter.collectionId)?.name ?? filter.collectionId}` : "", patch: { collectionId: "" } },
        ].filter(item => item.label).map(item => <Button className="filter-chip" size="small" type="tertiary" icon={<X size={12} />} iconPosition="right" key={item.key} onClick={() => updateFilter({ ...item.patch, page: 1 })} aria-label={`${t("移除筛选")} ${item.label}`}>{item.label}</Button>)}
      </div>}
      <div className={`gallery-result-bar${selectedEntries.length > 0 ? " gallery-result-bar-active" : ""}`}>
        <Checkbox checked={Boolean(visibleEntries.length) && selectedEntries.length === visibleEntries.length} indeterminate={selectedEntries.length > 0 && selectedEntries.length < visibleEntries.length} disabled={selectionDisabled || !visibleEntries.length} onChange={event => setSelected(event.target.checked ? new Set(visibleEntries.map(({ item }) => entryKey(item))) : new Set())}>{t("选择本页")}</Checkbox>
        <span className="text-xs text-fg-muted" aria-live="polite">{rangeError ? `${t("筛选结果")} —` : libraryQuery.isPlaceholderData && libraryQuery.isFetching ? t("正在应用筛选...") : `${t("筛选结果")} ${totalEntries} ${t("项")}`}</span>
        {selectedEntries.length > 0 && <div className="gallery-batch-actions"><span className="text-xs">{t("已选")} {selectedEntries.length}</span><Button size="small" type="tertiary" icon={<Copy size={14} />} disabled={selectionDisabled} onClick={() => void copyToClipboard(selectedEntries.map(({ item }) => item.filePath).join("\n"), t("所选文件路径"))}>{t("复制路径")}</Button><Button size="small" type="tertiary" icon={<Download size={14} />} disabled={selectionDisabled} onClick={downloadSelected}>{t("批量下载")}</Button><Button size="small" type="tertiary" icon={<Archive size={14} />} disabled={selectionDisabled || zipExportMutation.isPending} loading={zipExportMutation.isPending} onClick={() => zipExportMutation.mutate()}>{t("打包 ZIP")}</Button><Button size="small" type="tertiary" icon={<FolderPlus size={14} />} disabled={selectionDisabled} onClick={() => { setOrganizeLoaded(true); setSelectedCollectionId(""); setBatchCollectionOpen(true); }}>{t("加入合集")}</Button><Button size="small" type="tertiary" icon={<ExternalLink size={14} />} disabled={selectionDisabled} onClick={exportSelected}>{t("导出清单")}</Button><Button size="small" type="danger" icon={<Trash2 size={14} />} disabled={selectionDisabled} onClick={() => { const ids = selectedEntries.map(({ item }) => item.id).filter((id) => id > 0); if (ids.length > 0) { setDeleteLocalFiles(false); setDeletingIds(ids); } }}>{t("批量删除")}</Button><Button size="small" theme="borderless" type="tertiary" icon={<X size={14} />} aria-label={t("取消选择")} onClick={() => setSelected(new Set())} /></div>}
      </div>
      {rangeError && <Alert type="warning" message={rangeError} />}
      {libraryQuery.isError && libraryQuery.data && <Alert type="error" message={getErrorMessage(libraryQuery.error)} />}

      {libraryQuery.data?.truncated && (
        <Alert type="warning" message={t("媒体库索引已达上限，当前仅显示最近的记录")} />
      )}
      </>}

      {/* 按账号浏览：头像 + 名字的分页卡片网格，点卡片回到该账号的媒体列表 */}
      {viewMode === "users" ? (
        <>
          <div className="page-toolbar gallery-toolbar">
            <div className="toolbar-filters">
              <Input
                prefix={<Search size={16} />}
                aria-label={t("搜索账号或昵称...")}
                placeholder={t("搜索账号或昵称...")}
                value={usersSearch}
                onChange={(value) => {
                  setUsersSearch(value);
                  setUsersPage(1);
                }}
                showClear
                className="toolbar-search"
              />
              <span className="text-xs text-fg-muted" aria-live="polite">
                {t("共 {{value0}} 个账号", { value0: usersTotal })}
              </span>
            </div>
          </div>
          {usersQuery.isLoading ? (
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
          ) : usersItems.length === 0 ? (
            <Empty description={trimmedUsersSearch ? t("没有匹配的账号") : t("还没有可浏览的账号")} />
          ) : (
            <>
              <div
                className={`gallery-grid${usersQuery.isFetching ? " opacity-60 transition-opacity" : ""}`}
                aria-busy={usersQuery.isFetching}
              >
                {usersItems.map((summary) => (
                  <UserCard
                    key={summary.screenName}
                    summary={summary}
                    onOpen={openUserMedia}
                  />
                ))}
              </div>
              {usersTotalPages > 1 && (
                <div className="flex justify-center pt-3">
                  <Pagination
                    page={usersPage}
                    pageSize={LIBRARY_USERS_PAGE_SIZE}
                    total={usersTotal}
                    totalLabel={(total) => t("共 {{value0}} 个账号", { value0: total })}
                    onPageChange={(nextPage) => setUsersPage(Math.min(Math.max(1, nextPage), usersTotalPages))}
                  />
                </div>
              )}
            </>
          )}
        </>
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
          className={`gallery-${view} gallery-density-${density}${selectedEntries.length > 0 ? " gallery-selecting" : ""}${libraryQuery.isFetching ? " opacity-60 transition-opacity" : ""}`}
          aria-busy={libraryQuery.isFetching}
        >
          {visibleEntries.map(({ item, isVideo, isImage, isGif }, index) => {
            const key = entryKey(item);
            return (
            <div
              key={key}
              className={`gallery-entry${selected.has(key) ? " gallery-entry-selected" : ""}`}
              onClickCapture={(e) => { shiftSelectRef.current = e.shiftKey; }}
            >
            <Checkbox
              className="gallery-entry-checkbox"
              aria-label={`${t("选择文件")} ${displayFileName(item)}`}
              checked={selected.has(key)}
              disabled={selectionDisabled}
              onChange={event => {
                const checked = Boolean(event.target.checked);
                const useRange = Boolean((event.nativeEvent as MouseEvent | undefined)?.shiftKey || shiftSelectRef.current);
                shiftSelectRef.current = false;
                setSelected(current => {
                  const next = new Set(current);
                  const anchorKey = lastSelectedKeyRef.current;
                  const anchorIndex = useRange && anchorKey
                    ? visibleEntries.findIndex(entry => entryKey(entry.item) === anchorKey)
                    : -1;
                  if (anchorIndex >= 0) {
                    const [start, end] = anchorIndex < index ? [anchorIndex, index] : [index, anchorIndex];
                    for (let i = start; i <= end; i += 1) {
                      const rangeKey = entryKey(visibleEntries[i].item);
                      if (checked) next.add(rangeKey);
                      else next.delete(rangeKey);
                    }
                  } else if (checked) {
                    next.add(key);
                  } else {
                    next.delete(key);
                  }
                  return next;
                });
                lastSelectedKeyRef.current = key;
              }}
            />
            <GalleryCard
              key={item.id || item.filePath}
              item={item}
              isVideo={isVideo}
              isPreviewableImage={isImage || isGif}
              thumbnailFit={thumbnailFit}
              offlineThumbnails={offlineThumbnails}
              onOpen={openPreview}
              onToggleStar={handleToggleDownloadStar}
              onRequestDelete={handleRequestDeleteDownload}
            />
            </div>
            );
          })}
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
          setPreviewZoomed(false);
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
              <div className={`gallery-preview-frame relative flex items-center justify-center bg-black/95 p-2${!isVideo && previewZoomed ? " gallery-preview-frame-zoomed" : " overflow-hidden"}`}>
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
                    onClick={() => setPreviewZoomed(z => !z)}
                    className={previewZoomed ? "cursor-zoom-out" : "h-full w-full min-h-0 min-w-0 object-contain cursor-zoom-in"}
                  />
                )}

                {/* 左右切图浮动按钮（当前服务端分页内循环） */}
                <button
                  type="button"
                  aria-label={t("上一个媒体文件")}
                  disabled={filter.page === 1 && previewIndex === 0 || Boolean(pendingPreviewEdge)}
                  onClick={() => stepPreview(-1)}
                  className="gallery-preview-nav absolute left-3 top-1/2 -translate-y-1/2 flex size-10 items-center justify-center rounded-full bg-black/60 text-white hover:bg-black/90 transition-colors cursor-pointer"
                >
                  <ChevronLeft className="size-5" />
                </button>
                <button
                  type="button"
                  aria-label={t("下一个媒体文件")}
                  disabled={filter.page === totalPages && previewIndex === visibleEntries.length - 1 || Boolean(pendingPreviewEdge)}
                  onClick={() => stepPreview(1)}
                  className="gallery-preview-nav absolute right-3 top-1/2 -translate-y-1/2 flex size-10 items-center justify-center rounded-full bg-black/60 text-white hover:bg-black/90 transition-colors cursor-pointer"
                >
                  <ChevronRight className="size-5" />
                </button>
              </div>
              <div className="gallery-preview-details"><span className="tabular-nums">{pendingPreviewEdge || libraryQuery.isPlaceholderData ? t("加载中...") : `${((libraryQuery.data?.pagination.page ?? filter.page) - 1) * pageSize + previewIndex + 1} / ${totalEntries}`}</span><span className="text-fg-subtle">{isVideo ? t("按 ← / → 切换") : t("按 ← / → 切换，点击图片缩放")}</span>{item.jobId > 0 && <a href="/tasks">{t("来源任务")} #{item.jobId}</a>}{item.tweetId && <a href={`/tweets?id=${encodeURIComponent(item.tweetId)}`}>{t("archive.title")} <ExternalLink size={12} /></a>}<code title={item.filePath}>{item.filePath}</code></div>

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
                  {item.id > 0 && (
                    <>
                      <Tooltip title={item.starred ? t("取消收藏") : t("收藏媒体")}>
                        <Button
                          theme="borderless"
                          type={item.starred ? "warning" : "tertiary"}
                          icon={<Star size={16} fill={item.starred ? "currentColor" : "none"} />}
                          aria-label={item.starred ? t("取消收藏") : t("收藏媒体")}
                          onClick={() => handleToggleDownloadStar(item)}
                        />
                      </Tooltip>
                      <Tooltip title={t("设置标签与合集")}>
                        <Button
                          theme="borderless"
                          type="tertiary"
                          icon={<TagIcon size={16} />}
                          aria-label={t("设置标签与合集")}
                          onClick={() => {
                            setOrganizeLoaded(true);
                            setOrganizingDownload(item);
                            setSelectedTagIds((item.tags ?? []).map((tg) => tg.id));
                            setSelectedCollectionId("");
                          }}
                        />
                      </Tooltip>
                    </>
                  )}
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
                  {item.id > 0 && (
                    <Tooltip title={t("删除媒体")}>
                      <Button
                        theme="borderless"
                        type="danger"
                        icon={<Trash2 size={16} />}
                        aria-label={t("删除媒体")}
                        onClick={() => handleRequestDeleteDownload(item)}
                      />
                    </Tooltip>
                  )}
                </div>
              </div>
            </div>
          );
        })()}
      </MediaModal>

      {/* 删除媒体确认弹窗 */}
      <Modal
        open={Boolean(deletingIds && deletingIds.length > 0)}
        onClose={() => setDeletingIds(null)}
        title={t("删除媒体")}
        footer={
          <div className="flex justify-end gap-2">
            <Button type="tertiary" onClick={() => setDeletingIds(null)}>{t("取消")}</Button>
            <Button
              theme="solid"
              type="danger"
              loading={deleteMutation.isPending}
              onClick={() => deletingIds && deleteMutation.mutate({ ids: deletingIds, removeFiles: deleteLocalFiles })}
            >
              {t("确认删除")}
            </Button>
          </div>
        }
      >
        <div className="space-y-3 text-sm">
          <p>{t("确定删除选中的 {{value0}} 条媒体记录？", { value0: deletingIds?.length ?? 0 })}</p>
          <ControlCheckbox
            checked={deleteLocalFiles}
            onChange={setDeleteLocalFiles}
            label={t("同时删除本地磁盘上的对应文件（仅当无其他记录引用时）")}
          />
        </div>
      </Modal>

      {/* 单个媒体打标签与加入合集 */}
      <Modal
        open={Boolean(organizingDownload)}
        onClose={() => setOrganizingDownload(null)}
        title={t("设置标签与合集")}
        footer={
          <div className="flex justify-end gap-2">
            <Button type="tertiary" onClick={() => setOrganizingDownload(null)}>{t("取消")}</Button>
            <Button theme="solid" loading={saveOrganizeDownload.isPending} onClick={() => saveOrganizeDownload.mutate()}>{t("保存设置")}</Button>
          </div>
        }
      >
        <div className="space-y-4">
          <div>
            <div className="mb-2 text-xs font-semibold text-fg-muted">{t("选择标签")}</div>
            {tagsList.length === 0 ? (
              <p className="text-xs text-fg-subtle">{t("暂无自定义标签，请先点击“标签与合集”创建。")}</p>
            ) : (
              <div className="flex flex-wrap gap-3">
                {tagsList.map((tg) => (
                  <ControlCheckbox
                    key={tg.id}
                    checked={selectedTagIds.includes(tg.id)}
                    onChange={(checked) =>
                      setSelectedTagIds((current) =>
                        checked ? Array.from(new Set([...current, tg.id])) : current.filter((id) => id !== tg.id),
                      )
                    }
                    label={tg.name}
                  />
                ))}
              </div>
            )}
          </div>
          <Field label={t("加入合集 (可选)")}>
            <ControlSelect
              ariaLabel={t("加入合集")}
              value={selectedCollectionId}
              onChange={setSelectedCollectionId}
              options={[{ value: "", label: t("不加入合集") }, ...collectionsList.map((col) => ({ value: String(col.id), label: col.name }))]}
            />
          </Field>
        </div>
      </Modal>

      {/* 批量加入合集 */}
      <Modal
        open={batchCollectionOpen}
        onClose={() => setBatchCollectionOpen(false)}
        title={t("批量加入合集")}
        footer={
          <div className="flex justify-end gap-2">
            <Button type="tertiary" onClick={() => setBatchCollectionOpen(false)}>{t("取消")}</Button>
            <Button theme="solid" disabled={!selectedCollectionId} loading={batchAddCollection.isPending} onClick={() => batchAddCollection.mutate()}>{t("确认加入")}</Button>
          </div>
        }
      >
        <Field label={t("选择目标合集")}>
          <ControlSelect
            ariaLabel={t("选择目标合集")}
            value={selectedCollectionId}
            onChange={setSelectedCollectionId}
            options={[{ value: "", label: t("请选择合集") }, ...collectionsList.map((col) => ({ value: String(col.id), label: col.name }))]}
          />
        </Field>
      </Modal>

      <OrganizeManagerModal open={organizeOpen} onClose={() => setOrganizeOpen(false)} />
    </div>
  );
}
