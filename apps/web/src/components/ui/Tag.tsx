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

// 语义色 → 配色三元组（底色/文字/描边）。
// neutral 是 default 的历史别名（保留公开 Tone 类型以兼容调用方），
// 两者共用同一份类名，避免以后改一处忘另一处。
const NEUTRAL_TONE_CLASS = "bg-surface-muted text-fg-muted border-line";

const toneClass: Record<Tone, string> = {
  default: NEUTRAL_TONE_CLASS,
  neutral: NEUTRAL_TONE_CLASS,
  brand: "bg-brand-50 text-brand-700 border-brand-200",
  success: "bg-success-soft text-success-strong border-success/30",
  warning: "bg-warning-soft text-warning-strong border-warning/30",
  danger: "bg-danger-soft text-danger-strong border-danger/30",
  info: "bg-info-soft text-info-strong border-info/30",
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

const SUBTLE_DOT_CLASS = "bg-fg-subtle";

const dotClass: Record<Tone, string> = {
  default: SUBTLE_DOT_CLASS,
  neutral: SUBTLE_DOT_CLASS,
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
  // 实心底色必须配"配对前景色"（success/warning/danger/info 的 *-contrast），
  // 不能写死 text-white：深色钛金下 brand-500 近白，白字等于隐形。
  const toneStyle: Record<Tone, string> = {
    default: "bg-surface-hover text-fg-muted",
    neutral: "bg-surface-hover text-fg-muted",
    brand: "bg-brand-500 text-brand-contrast",
    success: "bg-success text-success-contrast",
    warning: "bg-warning text-warning-contrast",
    danger: "bg-danger text-danger-contrast",
    info: "bg-info text-info-contrast",
  };
  // 计数本身没有可读文本：可见数字标 aria-hidden，另给一个 sr-only 文本，
  // 读屏读成"12 条"而不是孤零零的"12"（aria-label 放在 generic span 上不保证被朗读）。
  const readableCount = count > overflowCount ? `${overflowCount} 条以上` : `${count} 条`;
  return (
    <span
      className={cn(
        "inline-flex min-w-4 items-center justify-center rounded-full px-1",
        "text-[10px] leading-4 font-semibold tabular-nums",
        toneStyle[tone],
        className,
      )}
    >
      <span aria-hidden="true">{count > overflowCount ? `${overflowCount}+` : count}</span>
      <span className="sr-only">{readableCount}</span>
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
        "bg-brand-50 text-sm font-bold text-brand-600",
        className,
      )}
      aria-hidden="true"
    >
      {children}
    </span>
  );
}
