import type { JobKind } from "./api";

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

export function formatIntervalMinutes(minutes: number) {
  if (!Number.isFinite(minutes) || minutes <= 0) {
    return "未设置";
  }
  if (minutes % 1440 === 0) {
    return `每 ${minutes / 1440} 天`;
  }
  if (minutes % 60 === 0) {
    return `每 ${minutes / 60} 小时`;
  }
  return `每 ${minutes} 分钟`;
}
