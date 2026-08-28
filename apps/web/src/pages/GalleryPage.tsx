import {
  LeftOutlined,
  CopyOutlined,
  LinkOutlined,
  PictureOutlined,
  RightOutlined,
} from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import {
  Button,
  Card,
  Col,
  Empty,
  Input,
  Modal,
  Pagination,
  Row,
  Segmented,
  Select,
  Space,
  Tooltip,
  Typography,
  notification,
} from "antd";
import React, { lazy, Suspense, useCallback, useDeferredValue, useEffect, useMemo, useState } from "react";
import {
  formatBytes,
  getLibraryDownloads,
  libraryDownloadsLimit,
  libraryDownloadsQueryRoot,
  type DownloadRecord,
  type Job,
} from "../lib/api";
import { formatDateTime } from "../components/common/CommonUI";

const ReactPlayer = lazy(() => import("react-player"));
const VIDEO_EXTENSIONS = [".mp4", ".mov", ".m4v", ".webm", ".ogv"];
const IMAGE_EXTENSIONS = [".jpg", ".jpeg", ".png", ".webp"];

export interface GalleryPageProps {
  jobs?: Job[];
  downloads?: DownloadRecord[];
}

const copyToClipboard = async (text: string, label = "路径") => {
  let copied = false;
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      copied = true;
    }
  } catch {
    copied = false;
  }
  if (!copied) {
    try {
      const textarea = document.createElement("textarea");
      textarea.value = text;
      textarea.style.position = "fixed";
      textarea.style.opacity = "0";
      document.body.appendChild(textarea);
      textarea.select();
      copied = document.execCommand("copy");
      textarea.remove();
    } catch {
      copied = false;
    }
  }
  if (copied) {
    notification.success({
      message: "复制成功",
      description: `已复制${label}到剪贴板`,
    });
  } else {
    notification.warning({
      message: "复制失败",
      description: `当前浏览器环境不支持自动复制，请手动复制${label}`,
    });
  }
};

// VideoPoster 依次尝试海报来源：记录里的预览图直链（浏览器直接加载，最快），
// 失败后回退服务端预览端点（服务端经代理回源并缓存到磁盘，之后命中本地文件），
// 全部失败才显示占位图标。旧实现失败后仅隐藏图片，历史媒体会永远停在占位图。
// 状态保存在单个卡片内，一张海报失败不会触发整页重渲染。
function VideoPoster({ item, fileName, onOpen }: { item: DownloadRecord; fileName: string; onOpen: () => void }) {
  const sources = useMemo(() => {
    const list: string[] = [];
    if (item.previewUrl && !/\.(mp4|mov|m4v|webm|ogv)(?:[?#]|$)/i.test(item.previewUrl)) {
      list.push(item.previewUrl);
    }
    if (item.id > 0) {
      list.push(`/api/library/downloads/${item.id}/preview`);
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
          <PictureOutlined className="text-4xl text-indigo-400" />
          <span className="text-xs">点击查看视频</span>
        </span>
        {sourceIndex < sources.length && (
          <img
            src={sources[sourceIndex]}
            alt={fileName}
            loading="lazy"
            decoding="async"
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
  index,
  onOpen,
}: {
  item: DownloadRecord;
  isVideo: boolean;
  isPreviewableImage: boolean;
  index: number;
  onOpen: (index: number) => void;
}) {
  const fileName = item.filePath.split(/[\\/]/).pop() || item.filePath;
  const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
  const previewURL = item.fileUrl || `/api/library/downloads/${item.id}/file`;
  return (
    <Card
      hoverable
      className="!rounded-xl !border-slate-200 dark:!border-slate-800 overflow-hidden shadow-xs"
      styles={{ body: { padding: "10px 12px" } }}
      cover={
        <div className="aspect-square bg-slate-100 dark:bg-slate-950 relative flex items-center justify-center overflow-hidden">
          {isVideo ? (
            <VideoPoster item={item} fileName={fileName} onOpen={() => onOpen(index)} />
          ) : isPreviewableImage ? (
            // 兜底图标垫在图片下层：文件缺失/加载失败时隐藏 img 露出占位，
            // 避免出现裂图。
            <span className="relative block h-full w-full">
              <span className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-slate-400">
                <PictureOutlined className="text-3xl" />
                <span className="text-[11px] font-mono">{ext}</span>
              </span>
              <img
                src={previewURL}
                alt={fileName}
                onClick={() => onOpen(index)}
                loading="lazy"
                decoding="async"
                onError={(event) => {
                  event.currentTarget.style.display = "none";
                }}
                className="relative z-10 h-full w-full cursor-pointer object-contain"
              />
            </span>
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
            className="text-[12px] block text-slate-800 dark:text-slate-200"
          >
            {fileName}
          </Typography.Text>
        </Tooltip>

        <Typography.Text
          type="secondary"
          ellipsis
          className="!text-[11px] !font-mono block"
        >
          {item.userScreenName
            ? `${item.userName || item.userScreenName}  @${item.userScreenName}`
            : "未识别用户"}
        </Typography.Text>

        <div className="flex items-center justify-between pt-1.5 border-t border-slate-100 dark:border-slate-800">
          <span className="text-[10px] text-slate-400 font-mono">
            {formatDateTime(item.createdAt)}
          </span>
          <Space size={2}>
            <Tooltip title="复制本地路径">
              <Button
                size="small"
                type="text"
                icon={<CopyOutlined className="text-xs" />}
                onClick={() => void copyToClipboard(item.filePath, "本地路径")}
                className="!h-6 !w-6 !p-0"
              />
            </Tooltip>
            <Tooltip title="复制媒体直链">
              <Button
                size="small"
                type="text"
                icon={<LinkOutlined className="text-xs" />}
                onClick={() => void copyToClipboard(item.mediaUrl, "原始下载直链")}
                className="!h-6 !w-6 !p-0"
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
  const [previewIndex, setPreviewIndex] = useState<number | null>(null);
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

  useEffect(() => {
    if (previewIndex !== null && previewIndex >= filteredEntries.length) {
      setPreviewIndex(null);
      return;
    }
    if (previewIndex === null) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        event.preventDefault();
        setPreviewIndex((current) => {
          if (current === null || filteredEntries.length === 0) return current;
          const delta = event.key === "ArrowRight" ? 1 : -1;
          return (current + delta + filteredEntries.length) % filteredEntries.length;
        });
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [filteredEntries.length, previewIndex]);

  // 稳定的 onOpen 引用是 GalleryCard memo 生效的前提。
  const openPreview = useCallback((index: number) => setPreviewIndex(index), []);

  return (
    <div className="space-y-5">
      {/* 顶部标题与筛选 (纯 Ant Design 组件) */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 pb-3 border-b border-slate-200 dark:border-slate-800/80">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100 tracking-tight">
            媒体归档库
          </h1>
          <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
            集中浏览所有已下载的高清图片、动态 GIF 与 4K 视频原片
          </p>
        </div>

        <Space size={10} wrap>
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
            className="!w-56"
            options={[{ value: "all", label: "全部用户" }, ...userOptions]}
            showSearch
            optionFilterProp="label"
          />

          <Input.Search
            placeholder="搜索文件名、推文 ID..."
            value={searchFilter}
            onChange={(e) => setSearchFilter(e.target.value)}
            allowClear
            className="!w-56"
          />
        </Space>
      </div>

      {filteredEntries.length === 0 ? (
        <Card className="!rounded-2xl !border-slate-200 dark:!border-slate-800 p-12 text-center">
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="暂无归档文件记录。完成下载任务后，媒体文件将自动在此呈现。"
          />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {visibleEntries.map(({ item, isVideo, isImage, isGif }, visibleIndex) => {
            const filteredIndex = (currentPage - 1) * pageSize + visibleIndex;
            return (
              <Col xs={24} sm={12} md={8} lg={6} xl={4} key={item.id || item.filePath}>
                <GalleryCard
                  item={item}
                  isVideo={isVideo}
                  isPreviewableImage={isImage || isGif}
                  index={filteredIndex}
                  onOpen={openPreview}
                />
              </Col>
            );
          })}
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
        open={previewIndex !== null}
        onCancel={() => setPreviewIndex(null)}
        footer={null}
        width="min(94vw, 1100px)"
        centered
        destroyOnHidden
        title={
          previewIndex === null
            ? undefined
            : (() => {
                const entry = filteredEntries[previewIndex];
                const item = entry?.item;
                return item
                  ? `${item.userName || "未识别用户"}${item.userScreenName ? `  @${item.userScreenName}` : ""}`
                  : undefined;
              })()
        }
      >
        {previewIndex !== null && filteredEntries[previewIndex] && (() => {
          const { item, isVideo } = filteredEntries[previewIndex];
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
                  onClick={() => setPreviewIndex((previewIndex - 1 + filteredEntries.length) % filteredEntries.length)}
                  className="!absolute !left-3 !top-1/2 !-translate-y-1/2 !bg-black/60 !text-white !border-white/30"
                  aria-label="上一个文件"
                />
              </Tooltip>
              <Tooltip title="下一个文件">
                <Button
                  shape="circle"
                  icon={<RightOutlined />}
                  onClick={() => setPreviewIndex((previewIndex + 1) % filteredEntries.length)}
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
