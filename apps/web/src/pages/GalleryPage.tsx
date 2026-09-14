import {
  LeftOutlined,
  CopyOutlined,
  LinkOutlined,
  PictureOutlined,
  RightOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Card,
  Col,
  Empty,
  Flex,
  Input,
  Modal,
  Pagination,
  Row,
  Segmented,
  Select,
  Skeleton,
  Space,
  Tooltip,
  Typography,
  notification,
} from "antd";
import React, {
  lazy,
  Suspense,
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

const ReactPlayer = lazy(() => import("react-player"));
const VIDEO_EXTENSIONS = [".mp4", ".mov", ".m4v", ".webm", ".ogv"];
const IMAGE_EXTENSIONS = [".jpg", ".jpeg", ".png", ".webp"];

// 预览弹窗按身份定位当前文件：下标会随刷新/搜索输入改变所指的记录，
// 弹窗开着时会静默把用户正在看的媒体换成另一份。历史记录可能没有 id，回退文件路径。
function entryKey(item: DownloadRecord) {
  return item.id > 0 ? String(item.id) : item.filePath;
}

export interface GalleryPageProps {
  jobs?: Job[];
  downloads?: DownloadRecord[];
}

// VideoPoster 依次尝试海报来源：优先服务端预览端点（经配置的代理回源并缓存到磁盘，
// 之后直接命中本地文件），失败后才回退记录里的预览图直链，全部失败才显示占位图标。
// 直链指向 twimg，浏览器直接加载会把访客 IP 与 Referer 暴露给 X 的 CDN，
// 也绕过了用户配置的代理，因此只作为兜底并禁止携带 Referer。
// 状态保存在单个卡片内，一张海报失败不会触发整页重渲染。
function VideoPoster({ item, fileName, onOpen }: { item: DownloadRecord; fileName: string; onOpen: () => void }) {
  const sources = useMemo(() => {
    const list: { url: string; remote: boolean }[] = [];
    if (item.id > 0) {
      list.push({ url: `/api/library/downloads/${item.id}/preview`, remote: false });
    }
    if (item.previewUrl && !/\.(mp4|mov|m4v|webm|ogv)(?:[?#]|$)/i.test(item.previewUrl)) {
      list.push({ url: item.previewUrl, remote: true });
    }
    return list;
  }, [item.previewUrl, item.id]);
  const [sourceIndex, setSourceIndex] = useState(0);

  return (
    <button
      type="button"
      className="group h-full w-full cursor-pointer border-0 bg-slate-900 p-0"
      onClick={onOpen}
      aria-label={`预览 ${fileName}`}
    >
      <span className="relative block h-full w-full">
        <span className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-slate-300">
          <PictureOutlined style={{ fontSize: 32, color: "var(--brand-200)" }} />
          <span className="text-xs">点击查看视频</span>
        </span>
        {sourceIndex < sources.length && (
          <img
            src={sources[sourceIndex].url}
            alt={fileName}
            loading="lazy"
            decoding="async"
            referrerPolicy={sources[sourceIndex].remote ? "no-referrer" : undefined}
            onError={() => setSourceIndex((current) => current + 1)}
            className="relative z-10 h-full w-full bg-slate-950 object-contain transition-transform group-hover:scale-[1.02]"
          />
        )}
      </span>
    </button>
  );
}

// GalleryCard 按记录逐卡片 memo：预览弹窗开关、翻页前后共用同一 item 引用，
// 父组件状态变化时未受影响的卡片直接跳过重渲染（60 张 antd Card 的开销可观）。
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
  const fileName = item.filePath.split(/[\\/]/).pop() || item.filePath;
  const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
  const previewURL = item.fileUrl || `/api/library/downloads/${item.id}/file`;
  return (
    <Card
      hoverable
      style={{ overflow: "hidden" }}
      styles={{ body: { padding: "10px 12px" } }}
      cover={
        <div className="aspect-square relative flex items-center justify-center overflow-hidden" style={{ background: "var(--app-surface-muted)" }}>
          {isVideo ? (
            <VideoPoster item={item} fileName={fileName} onOpen={() => onOpen(entryKey(item))} />
          ) : isPreviewableImage ? (
            // 兜底图标垫在图片下层：文件缺失/加载失败时隐藏 img 露出占位，
            // 避免出现裂图。用真实 button 承载点击，键盘与读屏可达。
            <button
              type="button"
              className="relative block h-full w-full cursor-pointer border-0 bg-transparent p-0"
              onClick={() => onOpen(entryKey(item))}
              aria-label={`预览 ${fileName}`}
            >
              <span className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-slate-400">
                <PictureOutlined className="text-3xl" />
                <span className="text-[11px] font-mono">{ext}</span>
              </span>
              <img
                src={previewURL}
                alt={fileName}
                loading="lazy"
                decoding="async"
                onError={(event) => {
                  event.currentTarget.style.display = "none";
                }}
                className="relative z-10 h-full w-full object-contain"
              />
            </button>
          ) : (
            <div className="flex flex-col items-center gap-1 text-slate-400">
              <PictureOutlined className="text-3xl text-slate-400" />
              <span className="text-[11px] font-mono font-medium">
                {ext} 文件
              </span>
            </div>
          )}

          <span className="absolute top-2 right-2 px-1.5 py-0.5 rounded bg-black/60 backdrop-blur-xs text-[10px] text-white font-mono">
            {ext} · {formatBytes(item.bytes)}
          </span>
        </div>
      }
    >
      <div className="space-y-1">
        <Tooltip title={item.filePath}>
          <Typography.Text
            strong
            ellipsis
            className="block" style={{ fontSize: 12 }}
          >
            {fileName}
          </Typography.Text>
        </Tooltip>

        <Typography.Text
          type="secondary"
          ellipsis
          className="font-mono block" style={{ fontSize: 11 }}
        >
          {item.userScreenName
            ? `${item.userName || item.userScreenName}  @${item.userScreenName}`
            : "未识别用户"}
        </Typography.Text>

        <div className="flex items-center justify-between pt-1.5" style={{ borderTop: "1px solid var(--app-border)" }}>
          <span className="font-mono" style={{ fontSize: 10, color: "var(--text-subtle)" }}>
            {formatDateTime(item.createdAt)}
          </span>
          <Space size={2}>
            <Tooltip title="复制本地路径">
              <Button
                size="small"
                type="text"
                icon={<CopyOutlined />}
                onClick={() => void copyToClipboard(item.filePath, "本地路径")}
              />
            </Tooltip>
            <Tooltip title="复制媒体直链">
              <Button
                size="small"
                type="text"
                icon={<LinkOutlined />}
                onClick={() => void copyToClipboard(item.mediaUrl, "原始下载直链")}
              />
            </Tooltip>
          </Space>
        </div>
      </div>
    </Card>
  );
});

export function GalleryPage({ jobs = [], downloads }: GalleryPageProps) {
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

  // 视频封面批量回填：运行中每 1.5s 轮询进度，空闲时不轮询。
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
      notification.error({ message: "封面补齐启动失败", description: String(error) });
    },
  });

  const backfillWasRunning = useRef(false);
  useEffect(() => {
    if (!backfillStatus) return;
    const wasRunning = backfillWasRunning.current;
    backfillWasRunning.current = backfillStatus.running;
    if (wasRunning && !backfillStatus.running && backfillStatus.total > 0) {
      if (backfillStatus.failed > 0) {
        notification.info({
          message: "封面补齐完成",
          description: `新增 ${backfillStatus.fetched} 张，跳过 ${backfillStatus.skipped} 张，失败 ${backfillStatus.failed} 张`,
        });
      } else {
        notification.success({
          message: "封面补齐完成",
          description: `新增 ${backfillStatus.fetched} 张，其余均已存在`,
        });
      }
      // 新海报落盘后重新拉一次列表，让卡片立即拿到可用封面。
      void queryClient.invalidateQueries({ queryKey: libraryDownloadsQueryRoot });
    }
  }, [backfillStatus, queryClient]);

  const allDownloads = downloads ?? libraryQuery.data ?? [];

  // Derive file metadata once per response. Filtering and category counters no
  // longer repeat extension parsing for every control update.
  const indexedDownloads = useMemo(
    () => allDownloads.map((item) => {
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

  // 下标由身份反查，列表变化后仍指向同一个文件；文件被筛掉/删除时关闭弹窗。
  const previewIndex = useMemo(
    () => (previewId === null ? -1 : filteredEntries.findIndex((entry) => entryKey(entry.item) === previewId)),
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
        const nextIndex = (previewIndex + delta + filteredEntries.length) % filteredEntries.length;
        setPreviewId(entryKey(filteredEntries[nextIndex].item));
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [filteredEntries, previewId, previewIndex]);

  // 稳定的 onOpen 引用是 GalleryCard memo 生效的前提。
  const openPreview = useCallback((nextId: string) => setPreviewId(nextId), []);
  const stepPreview = useCallback(
    (delta: number) => {
      if (filteredEntries.length === 0 || previewIndex === -1) return;
      const nextIndex = (previewIndex + delta + filteredEntries.length) % filteredEntries.length;
      setPreviewId(entryKey(filteredEntries[nextIndex].item));
    },
    [filteredEntries, previewIndex],
  );

  return (
    <div className="page-stack">
      {/* 顶部标题与筛选 */}
      <Flex
        className="page-header"
        align="center"
        justify="space-between"
        gap={16}
        wrap="wrap"
      >
        <div>
          <Typography.Title level={4} style={{ margin: 0 }}>
            媒体归档库
          </Typography.Title>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            集中浏览所有已下载的高清图片、动态 GIF 与 4K 视频原片
          </Typography.Text>
        </div>

        <Space wrap>
          <Segmented
            value={filterType}
            onChange={(val) => setFilterType(val as string)}
            options={[
              { label: `全部 (${categoryCounts.all})`, value: "all" },
              { label: `图片 (${categoryCounts.images})`, value: "images" },
              { label: `视频 (${categoryCounts.videos})`, value: "videos" },
              { label: `动图 (${categoryCounts.gifs})`, value: "gifs" },
            ]}
          />

          <Select
            value={userFilter}
            onChange={setUserFilter}
            style={{ width: 220 }}
            options={[{ value: "all", label: "全部用户" }, ...userOptions]}
            showSearch
            optionFilterProp="label"
          />

          <Input.Search
            placeholder="搜索文件名、推文 ID..."
            value={searchFilter}
            onChange={(e) => setSearchFilter(e.target.value)}
            allowClear
            style={{ width: 220 }}
          />

          <Tooltip title="扫描媒体库，为缺失封面的视频/GIF 重新拉取预览图并保存到本地；已有封面的记录会自动跳过">
            <Button
              icon={<PictureOutlined />}
              loading={backfillMutation.isPending}
              disabled={backfillRunning}
              onClick={() => backfillMutation.mutate()}
            >
              {backfillRunning && backfillStatus
                ? `补齐封面 ${backfillStatus.done}/${backfillStatus.total}`
                : "补齐视频封面"}
            </Button>
          </Tooltip>
        </Space>
      </Flex>

      {!downloads && libraryQuery.isLoading ? (
        <Row gutter={[16, 16]}>
          {Array.from({ length: 12 }, (_, index) => (
            <Col key={index} xs={24} sm={12} md={8} lg={6} xl={4}>
              <Card
                style={{ overflow: "hidden" }}
                styles={{ body: { padding: "10px 12px" } }}
                cover={
                  <Skeleton.Image
                    active
                    style={{ width: "100%", height: 140 }}
                  />
                }
              >
                <Skeleton active title={false} paragraph={{ rows: 1 }} />
              </Card>
            </Col>
          ))}
        </Row>
      ) : filteredEntries.length === 0 ? (
        <Card style={{ textAlign: "center", padding: 32 }}>
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="暂无归档文件记录。完成下载任务后，媒体文件将自动在此呈现。"
          />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {visibleEntries.map(({ item, isVideo, isImage, isGif }) => (
            <Col xs={24} sm={12} md={8} lg={6} xl={4} key={item.id || item.filePath}>
              <GalleryCard
                item={item}
                isVideo={isVideo}
                isPreviewableImage={isImage || isGif}
                onOpen={openPreview}
              />
            </Col>
          ))}
        </Row>
      )}

      {filteredEntries.length > pageSize && (
        <div className="flex justify-center pt-2">
          <Pagination
            current={currentPage}
            pageSize={pageSize}
            total={filteredEntries.length}
            showSizeChanger={false}
            showTotal={(total, range) => `${range[0]}-${range[1]} / ${total}`}
            onChange={setCurrentPage}
          />
        </div>
      )}

      <Modal
        open={previewEntry !== undefined}
        onCancel={() => setPreviewId(null)}
        footer={null}
        width="min(94vw, 1100px)"
        centered
        destroyOnHidden
        title={
          previewEntry
            ? `${previewEntry.item.userName || "未识别用户"}${previewEntry.item.userScreenName ? `  @${previewEntry.item.userScreenName}` : ""}`
            : undefined
        }
      >
        {previewEntry && (() => {
          const { item, isVideo } = previewEntry;
          const fileName = item.filePath.split(/[\\/]/).pop() || item.filePath;
          const previewURL = item.fileUrl || `/api/library/downloads/${item.id}/file`;
          return (
            <div className="relative flex min-h-[55vh] items-center justify-center bg-slate-950 rounded-lg overflow-hidden">
              {isVideo ? (
                <Suspense fallback={<span className="text-sm text-slate-300">正在加载播放器...</span>}>
                  <ReactPlayer
                    key={item.id}
                    src={previewURL}
                    playing
                    controls
                    playsInline
                    width="100%"
                    height="min(70vh, 680px)"
                    className="max-h-[70vh] max-w-full"
                  />
                </Suspense>
              ) : (
                <img src={previewURL} alt={fileName} loading="eager" className="max-h-[70vh] max-w-full object-contain" />
              )}
              <Tooltip title="上一个文件">
                <Button
                  shape="circle"
                  icon={<LeftOutlined />}
                  onClick={() => stepPreview(-1)}
                  className="!absolute !left-3 !top-1/2 !-translate-y-1/2 !bg-black/60 !text-white !border-white/30"
                  aria-label="上一个文件"
                />
              </Tooltip>
              <Tooltip title="下一个文件">
                <Button
                  shape="circle"
                  icon={<RightOutlined />}
                  onClick={() => stepPreview(1)}
                  className="!absolute !right-3 !top-1/2 !-translate-y-1/2 !bg-black/60 !text-white !border-white/30"
                  aria-label="下一个文件"
                />
              </Tooltip>
            </div>
          );
        })()}
      </Modal>
    </div>
  );
}
