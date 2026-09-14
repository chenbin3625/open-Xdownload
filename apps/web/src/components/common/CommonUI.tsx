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
  notification,
} from "antd";
import React, { useEffect, useState } from "react";
import type { JobKind } from "../../lib/api";

const { Text } = Typography;

export type TextTone = "secondary" | "success" | "warning" | "danger";

export const fullWidthStyle: React.CSSProperties = { width: "100%" };

export const defaultListPageSizeOptions = [5, 10, 20, 50];
export const tablePageSizeOptions = [10, 20, 50, 100];
export const failedTweetPageSizeOptions = [10, 20, 50];

export function AppEmpty({ description }: { description: string }) {
  return <Empty style={{ margin: "8px 0" }} image={Empty.PRESENTED_IMAGE_SIMPLE} description={description} />;
}

export function ListSkeleton({ rows = 4 }: { rows?: number }) {
  return (
    <Space orientation="vertical" size={10} style={fullWidthStyle}>
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
    <Space orientation="vertical" size={size} style={{ ...fullWidthStyle, ...style }}>
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
  simple = false,
  total,
}: {
  current: number;
  itemName: string;
  onChange: (page: number, pageSize: number) => void;
  pageSize: number;
  pageSizeOptions?: number[];
  simple?: boolean;
  total: number;
}) {
  return (
    <Flex justify="flex-end" style={fullWidthStyle}>
      <Pagination
        current={current}
        disabled={total === 0}
        pageSize={pageSize}
        pageSizeOptions={pageSizeOptions.map(String)}
        showSizeChanger={!simple}
        showTotal={simple ? undefined : (totalCount, range) =>
          totalCount > 0
            ? `共 ${totalCount} ${itemName}，当前 ${range[0]}-${range[1]}`
            : `共 0 ${itemName}`}
        simple={simple}
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
  simplePagination = false,
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
  simplePagination?: boolean;
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
          simple={simplePagination}
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
          simple={simplePagination}
          total={pagination.total}
          onChange={pagination.onChange}
        />
      </Stack>
    </LoadingSurface>
  );
}

// 明文 HTTP（局域网部署的常态）下 navigator.clipboard 是 undefined，
// 直接调用会在事件处理里抛 TypeError。这里逐级降级到 execCommand，
// 返回真实结果交给调用方提示，不再无条件报“已复制”。
export async function writeClipboard(text: string) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // 权限被拒时继续走 execCommand 兜底
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.select();
    const copied = document.execCommand("copy");
    textarea.remove();
    return copied;
  } catch {
    return false;
  }
}

export async function copyToClipboard(text: string, label = "路径") {
  const copied = await writeClipboard(text);
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
  return copied;
}

export function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);

  // 复用 writeClipboard 的降级链：此前这里自带一份实现，
  // 且无论 execCommand 是否成功都会亮起“已复制”。
  async function handleCopy() {
    if (await writeClipboard(value)) {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
      return;
    }
    setCopied(false);
    notification.warning({
      message: "复制失败",
      description: `当前浏览器环境不支持自动复制，请手动复制${label}`,
    });
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

// 表格每一行、媒体库每张卡片都会调用，formatter 提到模块级只构造一次。
const dateTimeFormatter = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
});

export function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return dateTimeFormatter.format(date);
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

// 各处 useMutation 的 onError 只有标题不同，正文一律是 getErrorMessage(error)。
// 用工厂函数下发处理器，调用点写成 onError: notifyError("取消失败")。
export function notifyError(title: string) {
  return (error: unknown) => {
    notification.error({
      message: title,
      description: getErrorMessage(error),
    });
  };
}
