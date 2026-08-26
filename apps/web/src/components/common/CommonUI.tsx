import {
  CheckCircleOutlined,
  CopyOutlined,
} from "@ant-design/icons";
import {
  Button,
  Empty,
  Flex,
  List,
  Pagination,
  Skeleton,
  Space,
  Spin,
  Tooltip,
  Typography,
} from "antd";
import React, { useEffect, useState } from "react";
import type { JobKind } from "../../lib/api";

const { Text } = Typography;

export type TextTone = "secondary" | "success" | "warning" | "danger";

export const fullWidthStyle: React.CSSProperties = { width: "100%" };

export const iconStyles = {
  primary: { color: "#1677ff", fontSize: 18 },
  success: { color: "#389e0d", fontSize: 18 },
  danger: { color: "#cf1322", fontSize: 18 },
} satisfies Record<string, React.CSSProperties>;

export const defaultListPageSizeOptions = [5, 10, 20, 50];
export const tablePageSizeOptions = [10, 20, 50, 100];
export const failedTweetPageSizeOptions = [10, 20, 50];

export function AppEmpty({ description }: { description: string }) {
  return <Empty style={{ margin: "8px 0" }} image={Empty.PRESENTED_IMAGE_SIMPLE} description={description} />;
}

export function ListSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <Space direction="vertical" size={10} style={fullWidthStyle}>
      {Array.from({ length: rows }, (_, index) => (
        <Skeleton
          active
          avatar
          key={index}
          paragraph={{ rows: 2 }}
          title={{ width: index % 2 === 0 ? "42%" : "58%" }}
        />
      ))}
    </Space>
  );
}

export function LoadingSurface({
  children,
  loading,
  tip = "加载中",
}: {
  children: React.ReactNode;
  loading?: boolean;
  tip?: string;
}) {
  return (
    <Spin spinning={!!loading} tip={loading ? tip : undefined}>
      <div style={{ minWidth: 0 }}>{children}</div>
    </Spin>
  );
}

export function Stack({
  children,
  size = 12,
  style,
}: {
  children: React.ReactNode;
  size?: number;
  style?: React.CSSProperties;
}) {
  return (
    <Space direction="vertical" size={size} style={{ ...fullWidthStyle, ...style }}>
      {children}
    </Space>
  );
}

export function Toolbar({ children }: { children: React.ReactNode }) {
  return (
    <Flex align="center" justify="space-between" gap={10} wrap="wrap" style={fullWidthStyle}>
      {children}
    </Flex>
  );
}

export function EllipsisText({
  children,
  code,
  style,
  title,
  type,
}: {
  children: React.ReactNode;
  code?: boolean;
  style?: React.CSSProperties;
  title?: string;
  type?: TextTone;
}) {
  return (
    <Text
      code={code}
      type={type}
      title={title}
      style={{
        display: "block",
        maxWidth: "100%",
        overflow: "hidden",
        textOverflow: "ellipsis",
        whiteSpace: "nowrap",
        ...style,
      }}
    >
      {children}
    </Text>
  );
}

export function useClientPagination<TItem>(items: TItem[], initialPageSize = 5) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(initialPageSize);
  const total = items.length;
  const maxPage = Math.max(1, Math.ceil(total / pageSize));
  const currentPage = Math.min(page, maxPage);

  useEffect(() => {
    setPage((current) => Math.min(current, maxPage));
  }, [maxPage]);

  const start = (currentPage - 1) * pageSize;
  const pagedItems = items.slice(start, start + pageSize);

  function handleChange(nextPage: number, nextPageSize?: number) {
    if (nextPageSize && nextPageSize !== pageSize) {
      setPageSize(nextPageSize);
      setPage(1);
      return;
    }
    setPage(nextPage);
  }

  return {
    items: pagedItems,
    page: currentPage,
    pageSize,
    total,
    onChange: handleChange,
  };
}

export function AppPagination({
  current,
  itemName,
  onChange,
  pageSize,
  pageSizeOptions = defaultListPageSizeOptions,
  total,
}: {
  current: number;
  itemName: string;
  onChange: (page: number, pageSize: number) => void;
  pageSize: number;
  pageSizeOptions?: number[];
  total: number;
}) {
  return (
    <Flex justify="flex-end" style={fullWidthStyle}>
      <Pagination
        current={current}
        disabled={total === 0}
        pageSize={pageSize}
        pageSizeOptions={pageSizeOptions.map(String)}
        showSizeChanger
        showTotal={(totalCount, range) =>
          totalCount > 0
            ? `共 ${totalCount} ${itemName}，当前 ${range[0]}-${range[1]}`
            : `共 0 ${itemName}`
        }
        size="small"
        total={total}
        onChange={onChange}
      />
    </Flex>
  );
}

export function PaginatedList<TItem>({
  bordered,
  emptyDescription,
  itemName,
  items,
  loading,
  maxHeight,
  pageSize = 5,
  renderItem,
  skeletonRows = pageSize,
  size = "default",
}: {
  bordered?: boolean;
  emptyDescription: string;
  itemName: string;
  items: TItem[];
  loading?: boolean;
  maxHeight?: number;
  pageSize?: number;
  renderItem: (item: TItem) => React.ReactNode;
  skeletonRows?: number;
  size?: "small" | "default" | "large";
}) {
  const pagination = useClientPagination(items, pageSize);
  const listStyle = maxHeight ? { maxHeight, overflow: "auto" } : undefined;

  if (loading && items.length === 0) {
    return <ListSkeleton rows={skeletonRows} />;
  }

  if (items.length === 0) {
    return (
      <Stack size={8}>
        <AppEmpty description={emptyDescription} />
        <AppPagination
          current={pagination.page}
          itemName={itemName}
          pageSize={pagination.pageSize}
          total={pagination.total}
          onChange={pagination.onChange}
        />
      </Stack>
    );
  }

  return (
    <LoadingSurface loading={loading}>
      <Stack size={8}>
        <List
          bordered={bordered}
          dataSource={pagination.items}
          locale={{ emptyText: <AppEmpty description={emptyDescription} /> }}
          renderItem={renderItem}
          size={size}
          style={listStyle}
        />
        <AppPagination
          current={pagination.page}
          itemName={itemName}
          pageSize={pagination.pageSize}
          total={pagination.total}
          onChange={pagination.onChange}
        />
      </Stack>
    </LoadingSurface>
  );
}

export function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);

  async function handleCopy() {
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(value);
      } else {
        const textarea = document.createElement("textarea");
        textarea.value = value;
        textarea.style.position = "fixed";
        textarea.style.opacity = "0";
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        document.body.removeChild(textarea);
      }
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      setCopied(false);
    }
  }

  return (
    <Tooltip title={copied ? "已复制" : label}>
      <Button
        size="small"
        type={copied ? "primary" : "text"}
        icon={copied ? <CheckCircleOutlined /> : <CopyOutlined />}
        onClick={handleCopy}
      />
    </Tooltip>
  );
}

export function formatIntervalMinutes(minutes: number) {
  if (!Number.isFinite(minutes) || minutes <= 0) {
    return "未设置";
  }
  if (minutes % 1440 === 0) {
    const days = minutes / 1440;
    return `每 ${days} 天`;
  }
  if (minutes % 60 === 0) {
    const hours = minutes / 60;
    return `每 ${hours} 小时`;
  }
  return `每 ${minutes} 分钟`;
}

export function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

export function clampPercent(value: number) {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(100, Math.max(0, Math.round(value * 100)));
}

export function kindLabel(kind: JobKind) {
  const labels: Record<string, string> = {
    tweet_link: "推文链接",
    media_url: "媒体地址",
    user: "用户",
    list: "列表",
    following: "关注",
    failed_retry: "失败重试",
  };
  return labels[kind] ?? "未知类型";
}

export function mediaTypeLabel(type: "photo" | "video" | "animated_gif" | "file") {
  return {
    photo: "图片",
    video: "视频",
    animated_gif: "GIF",
    file: "文件",
  }[type];
}

export function getErrorMessage(error: unknown) {
  if (error instanceof Error) {
    return error.message;
  }
  if (typeof error === "string") {
    return error;
  }
  return "未知错误";
}
