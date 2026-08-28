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
import React, { lazy, Suspense, useDeferredValue, useEffect, useMemo, useState } from "react";
import {
  formatBytes,
  getLibraryDownloads,
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
    queryFn: ({ signal }) => getLibraryDownloads(10000, signal),
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

  const filteredDownloads = useMemo(() => {
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
    }).map((entry) => entry.item);
  }, [deferredSearchFilter, filterType, indexedDownloads, userFilter]);

  useEffect(() => {
    setCurrentPage(1);
  }, [filterType, searchFilter, userFilter]);

  const visibleDownloads = useMemo(
    () => filteredDownloads.slice((currentPage - 1) * pageSize, currentPage * pageSize),
    [currentPage, filteredDownloads],
  );

  useEffect(() => {
    if (previewIndex !== null && previewIndex >= filteredDownloads.length) {
      setPreviewIndex(null);
      return;
    }
    if (previewIndex === null) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
        event.preventDefault();
        setPreviewIndex((current) => {
          if (current === null || filteredDownloads.length === 0) return current;
          const delta = event.key === "ArrowRight" ? 1 : -1;
          return (current + delta + filteredDownloads.length) % filteredDownloads.length;
        });
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [filteredDownloads.length, previewIndex]);

  const copyToClipboard = (text: string, label = "路径") => {
    void navigator.clipboard.writeText(text);
    notification.success({
      message: "复制成功",
      description: `已复制${label}到剪贴板`,
    });
  };

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

      {filteredDownloads.length === 0 ? (
        <Card className="!rounded-2xl !border-slate-200 dark:!border-slate-800 p-12 text-center">
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="暂无归档文件记录。完成下载任务后，媒体文件将自动在此呈现。"
          />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {visibleDownloads.map((item, visibleIndex) => {
            const filteredIndex = (currentPage - 1) * pageSize + visibleIndex;
            const fileName = item.filePath.split(/[\\/]/).pop() || item.filePath;
            const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
            const isVideo = ["MP4", "MOV", "M4V", "WEBM", "OGV"].includes(ext);
            const isPreviewableImage = ["JPG", "JPEG", "PNG", "WEBP", "GIF"].includes(ext);
            const previewURL = item.fileUrl || `/api/library/downloads/${item.id}/file`;
            // Use the CDN poster directly. The API preview endpoint is kept for
            // explicit callers, but routing every card through it causes one
            // extra request (and often a remote fetch) per video on first paint.
            const posterURL = item.previewUrl &&
              !/\.(mp4|mov|m4v|webm|ogv)(?:[?#]|$)/i.test(item.previewUrl)
              ? item.previewUrl
              : undefined;

            return (
              <Col xs={24} sm={12} md={8} lg={6} xl={4} key={item.id || item.filePath}>
                <Card
                  hoverable
                  className="!rounded-xl !border-slate-200 dark:!border-slate-800 overflow-hidden shadow-xs"
                  styles={{ body: { padding: "10px 12px" } }}
                  cover={
                    <div className="aspect-square bg-slate-100 dark:bg-slate-950 relative flex items-center justify-center overflow-hidden">
                      {isVideo ? (
                        <button
                          type="button"
                          className="group h-full w-full cursor-pointer border-0 bg-slate-900 p-0"
                          onClick={() => setPreviewIndex(filteredIndex)}
                          aria-label={`预览 ${fileName}`}
                        >
                          {posterURL ? (
                            <span className="relative block h-full w-full">
                              <span className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-slate-300">
                                <PictureOutlined className="text-4xl text-indigo-400" />
                                <span className="text-xs">点击查看视频</span>
                              </span>
                              <img
                                src={posterURL}
                                alt={fileName}
                                loading="lazy"
                                decoding="async"
                                onError={(event) => {
                                  event.currentTarget.style.display = "none";
                                }}
                                className="relative z-10 h-full w-full bg-slate-950 object-contain transition-transform group-hover:scale-[1.02]"
                              />
                            </span>
                          ) : (
                            <span className="flex h-full w-full flex-col items-center justify-center gap-2 text-slate-300">
                              <PictureOutlined className="text-4xl text-indigo-400" />
                              <span className="text-xs">点击查看视频</span>
                            </span>
                          )}
                        </button>
                      ) : isPreviewableImage ? (
                        <img
                          src={previewURL}
                          alt={fileName}
                          onClick={() => setPreviewIndex(filteredIndex)}
                          loading="lazy"
                          decoding="async"
                          className="h-full w-full cursor-pointer object-contain"
                        />
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
                            onClick={() => copyToClipboard(item.filePath, "本地路径")}
                            className="!h-6 !w-6 !p-0"
                          />
                        </Tooltip>
                        <Tooltip title="复制媒体直链">
                          <Button
                            size="small"
                            type="text"
                            icon={<LinkOutlined className="text-xs" />}
                            onClick={() => copyToClipboard(item.mediaUrl, "原始下载直链")}
                            className="!h-6 !w-6 !p-0"
                          />
                        </Tooltip>
                      </Space>
                    </div>
                  </div>
                </Card>
              </Col>
            );
          })}
        </Row>
      )}

      {filteredDownloads.length > pageSize && (
        <div className="flex justify-center pt-2">
          <Pagination
            current={currentPage}
            pageSize={pageSize}
            total={filteredDownloads.length}
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
                const item = filteredDownloads[previewIndex];
                return item
                  ? `${item.userName || "未识别用户"}${item.userScreenName ? `  @${item.userScreenName}` : ""}`
                  : undefined;
              })()
        }
      >
        {previewIndex !== null && filteredDownloads[previewIndex] && (() => {
          const item = filteredDownloads[previewIndex];
          const fileName = item.filePath.split(/[\\/]/).pop() || item.filePath;
          const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
          const previewURL = item.fileUrl || `/api/library/downloads/${item.id}/file`;
          const isVideo = ["MP4", "MOV", "M4V", "WEBM", "OGV"].includes(ext);
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
                  onClick={() => setPreviewIndex((previewIndex - 1 + filteredDownloads.length) % filteredDownloads.length)}
                  className="!absolute !left-3 !top-1/2 !-translate-y-1/2 !bg-black/60 !text-white !border-white/30"
                  aria-label="上一个文件"
                />
              </Tooltip>
              <Tooltip title="下一个文件">
                <Button
                  shape="circle"
                  icon={<RightOutlined />}
                  onClick={() => setPreviewIndex((previewIndex + 1) % filteredDownloads.length)}
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
