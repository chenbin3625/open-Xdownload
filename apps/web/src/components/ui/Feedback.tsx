import { AlertCircle, CheckCircle2, Info, Inbox, TriangleAlert } from "lucide-react";
import React from "react";
import { cn } from "../../lib/cn";
import type { Tone } from "./Tag";

const alertTone: Record<"info" | "success" | "warning" | "error", { box: string; icon: React.ReactNode }> = {
  info: {
    box: "bg-info-soft border-info/30 text-fg-body",
    icon: <Info className="size-4 shrink-0 text-info" />,
  },
  success: {
    box: "bg-success-soft border-success/30 text-fg-body",
    icon: <CheckCircle2 className="size-4 shrink-0 text-success" />,
  },
  warning: {
    box: "bg-warning-soft border-warning/30 text-fg-body",
    icon: <TriangleAlert className="size-4 shrink-0 text-warning" />,
  },
  error: {
    box: "bg-danger-soft border-danger/30 text-fg-body",
    icon: <AlertCircle className="size-4 shrink-0 text-danger" />,
  },
};

export function Alert({
  type = "info",
  message,
  description,
  action,
  showIcon = true,
  className,
}: {
  type?: "info" | "success" | "warning" | "error";
  message: React.ReactNode;
  description?: React.ReactNode;
  action?: React.ReactNode;
  showIcon?: boolean;
  className?: string;
}) {
  const tone = alertTone[type];
  return (
    <div
      // error 用 alert 角色即时播报，其余用 status 避免打断读屏
      role={type === "error" ? "alert" : "status"}
      className={cn(
        "flex items-start gap-2.5 rounded-card border px-4 py-3 text-sm",
        tone.box,
        className,
      )}
    >
      {showIcon && <span className="mt-0.5">{tone.icon}</span>}
      <div className="min-w-0 flex-1">
        <div className="font-medium text-fg">{message}</div>
        {description && <div className="mt-0.5 text-xs text-fg-muted">{description}</div>}
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      aria-hidden="true"
      className={cn("animate-skeleton rounded-md bg-surface-hover", className)}
    />
  );
}

// 列表骨架：标题宽度交替，避免整列等宽显得呆板。
export function ListSkeleton({ rows = 4, avatar = true }: { rows?: number; avatar?: boolean }) {
  return (
    <div className="flex flex-col gap-3" role="status" aria-label="加载中">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="flex items-start gap-3">
          {avatar && <Skeleton className="size-9 shrink-0 rounded-full" />}
          <div className="flex min-w-0 flex-1 flex-col gap-2">
            <Skeleton className={cn("h-3.5", index % 2 === 0 ? "w-2/5" : "w-3/5")} />
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-4/5" />
          </div>
        </div>
      ))}
    </div>
  );
}

export function Empty({
  description,
  children,
  className,
}: {
  description: React.ReactNode;
  children?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-center justify-center gap-2 py-8", className)}>
      <Inbox className="size-8 text-fg-subtle" aria-hidden="true" />
      <p className="max-w-md text-center text-xs text-fg-muted">{description}</p>
      {children}
    </div>
  );
}

const progressTone: Record<"normal" | "active" | "success" | "exception", string> = {
  normal: "bg-fg-subtle",
  active: "bg-brand-500",
  success: "bg-success",
  exception: "bg-danger",
};

export function Progress({
  percent,
  status = "normal",
  className,
}: {
  percent: number;
  status?: "normal" | "active" | "success" | "exception";
  className?: string;
}) {
  const clamped = Math.min(100, Math.max(0, percent));
  return (
    <div
      role="progressbar"
      aria-valuenow={clamped}
      aria-valuemin={0}
      aria-valuemax={100}
      className={cn("h-1.5 w-full overflow-hidden rounded-full bg-surface-hover", className)}
    >
      <div
        className={cn(
          "h-full rounded-full transition-[width] duration-300 ease-out",
          progressTone[status],
          // active 态叠一层斜纹流动，表达“正在进行”
          status === "active" &&
            "bg-[linear-gradient(90deg,var(--color-brand-500)_0%,var(--color-brand-400)_50%,var(--color-brand-500)_100%)]",
        )}
        style={{ width: `${clamped}%` }}
      />
    </div>
  );
}

export function Divider({ className }: { className?: string }) {
  return <hr className={cn("my-3 border-0 border-t border-line", className)} />;
}

// 键值信息网格，对应 antd Descriptions 的 items + 响应式 column。
export function Descriptions({
  items,
  columns = 3,
  className,
}: {
  items: { key: string; label: React.ReactNode; children: React.ReactNode }[];
  columns?: 1 | 2 | 3;
  className?: string;
}) {
  const columnClass = {
    1: "sm:grid-cols-1",
    2: "sm:grid-cols-2",
    3: "sm:grid-cols-2 md:grid-cols-3",
  }[columns];
  return (
    <dl className={cn("grid grid-cols-1 gap-x-6 gap-y-2.5", columnClass, className)}>
      {items.map((item) => (
        <div key={item.key} className="min-w-0">
          <dt className="text-[11px] text-fg-subtle">{item.label}</dt>
          <dd className="mt-0.5 min-w-0 text-xs text-fg-body">{item.children}</dd>
        </div>
      ))}
    </dl>
  );
}

export type { Tone };
