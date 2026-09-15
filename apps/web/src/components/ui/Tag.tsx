import React from "react";
import { cn } from "../../lib/cn";

export type Tone =
  | "default"
  | "neutral"
  | "brand"
  | "success"
  | "warning"
  | "danger"
  | "info";

// 语义色 → 配色三元组（底色/文字/描边）。暗色下的 soft 底色在
// index.css 里已改为低透明度叠加，因此这里不需要写 dark: 变体。
const toneClass: Record<Tone, string> = {
  default: "bg-surface-muted text-fg-muted border-line",
  neutral: "bg-surface-muted text-fg-muted border-line",
  brand: "bg-brand-50 text-brand-700 border-brand-200 dark:bg-brand-500/12 dark:text-brand-300 dark:border-brand-500/30",
  success: "bg-success-soft text-success border-success/30",
  warning: "bg-warning-soft text-warning border-warning/30",
  danger: "bg-danger-soft text-danger border-danger/30",
  info: "bg-info-soft text-info border-info/30",
};

export function Tag({
  children,
  tone = "default",
  icon,
  size = "md",
  className,
  ...rest
}: {
  children?: React.ReactNode;
  tone?: Tone;
  icon?: React.ReactNode;
  size?: "sm" | "md";
  className?: string;
} & React.HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-md border font-medium whitespace-nowrap select-none",
        size === "sm" ? "h-5 px-1.5 text-[10px] leading-none" : "h-6 px-2 text-xs leading-none",
        toneClass[tone],
        className,
      )}
      {...rest}
    >
      {icon}
      {children}
    </span>
  );
}

const dotClass: Record<Tone, string> = {
  default: "bg-fg-subtle",
  neutral: "bg-fg-subtle",
  brand: "bg-brand-500",
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-danger",
  info: "bg-info",
};

// 状态点。processing 语义用 ping 动画表达“进行中”，对应 antd 的 Badge status。
export function StatusDot({
  tone = "default",
  pulse = false,
  className,
}: {
  tone?: Tone;
  pulse?: boolean;
  className?: string;
}) {
  return (
    <span className={cn("relative inline-flex size-1.5 shrink-0", className)}>
      <span className={cn("size-1.5 rounded-full", dotClass[tone])} />
      {pulse && (
        <span
          className={cn(
            "absolute inset-0 animate-ping rounded-full opacity-60 motion-reduce:animate-none",
            dotClass[tone],
          )}
        />
      )}
    </span>
  );
}

// 计数气泡，对应 antd Badge 的 count + overflowCount。
export function CountBadge({
  count,
  overflowCount = 99,
  tone = "danger",
  className,
}: {
  count: number;
  overflowCount?: number;
  tone?: Tone;
  className?: string;
}) {
  if (count <= 0) return null;
  const toneStyle: Record<Tone, string> = {
    default: "bg-surface-hover text-fg-muted",
    neutral: "bg-surface-hover text-fg-muted",
    brand: "bg-brand-500 text-white",
    success: "bg-success text-white",
    warning: "bg-warning text-white",
    danger: "bg-danger text-white",
    info: "bg-info text-white",
  };
  return (
    <span
      className={cn(
        "inline-flex min-w-4 items-center justify-center rounded-full px-1",
        "text-[10px] leading-4 font-semibold tabular-nums",
        toneStyle[tone],
        className,
      )}
    >
      {count > overflowCount ? `${overflowCount}+` : count}
    </span>
  );
}

export function Avatar({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex size-8 shrink-0 items-center justify-center rounded-full",
        "bg-brand-50 text-sm font-bold text-brand-600 dark:bg-brand-500/15 dark:text-brand-300",
        className,
      )}
      aria-hidden="true"
    >
      {children}
    </span>
  );
}
