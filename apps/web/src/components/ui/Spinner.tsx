import React from "react";
import { cn } from "../../lib/cn";

// 纯 SVG 转圈，不依赖图标库；currentColor 让它自动跟随父级文字色。
export function Spinner({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
      className={cn("size-4 shrink-0 animate-[spin_0.7s_linear_infinite]", className)}
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="2.5" opacity="0.2" />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        stroke="currentColor"
        strokeWidth="2.5"
        strokeLinecap="round"
      />
    </svg>
  );
}

// 覆盖式加载态：内容变暗并禁用交互，中间浮出转圈 + 可选文案。
// 对应 antd Spin 的 nested 模式（用于列表/树等区域级加载）。
export function LoadingOverlay({
  children,
  loading,
  tip,
  className,
}: {
  children: React.ReactNode;
  loading?: boolean;
  tip?: string;
  className?: string;
}) {
  return (
    <div className={cn("relative min-w-0", className)}>
      <div
        className={cn(
          "min-w-0 transition-opacity duration-200",
          loading && "pointer-events-none opacity-45",
        )}
      >
        {children}
      </div>
      {loading && (
        <div
          className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-2"
          role="status"
          aria-live="polite"
        >
          <Spinner className="size-6 text-brand-500" />
          {tip && <span className="text-xs text-fg-muted">{tip}</span>}
        </div>
      )}
    </div>
  );
}
