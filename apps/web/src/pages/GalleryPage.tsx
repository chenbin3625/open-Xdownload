import {
  CopyOutlined,
  EyeOutlined,
  FolderOpenOutlined,
  LinkOutlined,
  PictureOutlined,
  VideoCameraOutlined,
} from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import {
  Button,
  Card,
  Col,
  Empty,
  Image,
  Input,
  Row,
  Segmented,
  Space,
  Tag,
  Tooltip,
  Typography,
  notification,
} from "antd";
import React, { useMemo, useState } from "react";
import {
  formatBytes,
  getLibraryDownloads,
  libraryDownloadsQueryRoot,
  type DownloadRecord,
  type Job,
} from "../lib/api";
import { formatDateTime } from "../components/common/CommonUI";

export interface GalleryPageProps {
  jobs?: Job[];
  downloads?: DownloadRecord[];
}

export function GalleryPage({ jobs = [], downloads }: GalleryPageProps) {
  const [filterType, setFilterType] = useState<string>("all");
  const [searchFilter, setSearchFilter] = useState<string>("");

  const libraryQuery = useQuery({
    queryKey: libraryDownloadsQueryRoot,
    queryFn: ({ signal }) => getLibraryDownloads(150, signal),
    staleTime: 30_000,
    enabled: !downloads,
  });

  const allDownloads = downloads ?? libraryQuery.data ?? [];

  const filteredDownloads = useMemo(() => {
    return allDownloads.filter((item) => {
      const lower = item.filePath.toLowerCase();
      const isVideo =
        lower.endsWith(".mp4") || lower.endsWith(".mov") || lower.endsWith(".m4v");
      const isGif = lower.endsWith(".gif");
      const isImage =
        lower.endsWith(".jpg") ||
        lower.endsWith(".jpeg") ||
        lower.endsWith(".png") ||
        lower.endsWith(".webp");

      if (filterType === "images" && !isImage) return false;
      if (filterType === "videos" && !isVideo) return false;
      if (filterType === "gifs" && !isGif) return false;

      if (searchFilter.trim()) {
        const kw = searchFilter.trim().toLowerCase();
        return (
          item.filePath.toLowerCase().includes(kw) ||
          item.mediaUrl.toLowerCase().includes(kw) ||
          String(item.tweetId).includes(kw)
        );
      }

      return true;
    });
  }, [allDownloads, filterType, searchFilter]);

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
              { label: `全部 (${allDownloads.length})`, value: "all" },
              { label: "图片", value: "images" },
              { label: "视频", value: "videos" },
              { label: "动图 (GIF)", value: "gifs" },
            ]}
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
          {filteredDownloads.map((item) => {
            const fileName = item.filePath.split("/").pop() || item.filePath;
            const ext = fileName.split(".").pop()?.toUpperCase() || "FILE";
            const isVideo = ext === "MP4" || ext === "MOV";

            return (
              <Col xs={24} sm={12} md={8} lg={6} xl={4} key={item.id}>
                <Card
                  hoverable
                  className="!rounded-xl !border-slate-200 dark:!border-slate-800 overflow-hidden shadow-xs"
                  styles={{ body: { padding: "10px 12px" } }}
                  cover={
                    <div className="aspect-square bg-slate-100 dark:bg-slate-950 relative flex items-center justify-center overflow-hidden">
                      {isVideo ? (
                        <div className="flex flex-col items-center gap-1 text-slate-400">
                          <VideoCameraOutlined className="text-3xl text-indigo-400" />
                          <span className="text-[11px] font-mono font-medium">
                            {ext} 视频
                          </span>
                        </div>
                      ) : (
                        <div className="flex flex-col items-center gap-1 text-slate-400">
                          <PictureOutlined className="text-3xl text-sky-400" />
                          <span className="text-[11px] font-mono font-medium">
                            {ext} 图片
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
                      推文: {item.tweetId}
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
    </div>
  );
}
