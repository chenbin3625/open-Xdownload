import { AlertCircle, CheckCircle2, Info, TriangleAlert, X } from "lucide-react";
import React, { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/cn";

export type ToastVariant = "success" | "error" | "warning" | "info";

export interface ToastOptions {
  message: React.ReactNode;
  description?: React.ReactNode;
  duration?: number;
}

interface ToastRecord extends ToastOptions {
  id: number;
  variant: ToastVariant;
}

const DEFAULT_DURATION = 4500;

// antd 的 notification 是模块级命令式 API，被大量 mutation 回调直接调用
// （回调在 React 树之外，拿不到 hook）。这里保持同样的形态：
// 模块级 store + 单例订阅，<Toaster /> 只负责渲染。
let toasts: ToastRecord[] = [];
let nextId = 1;
const listeners = new Set<(items: ToastRecord[]) => void>();

function emit() {
  // 传新数组，保证 useState 能识别为变更
  const snapshot = [...toasts];
  for (const listener of listeners) listener(snapshot);
}

function dismiss(id: number) {
  toasts = toasts.filter((item) => item.id !== id);
  emit();
}

function push(variant: ToastVariant, options: ToastOptions) {
  const id = nextId++;
  toasts = [...toasts, { ...options, id, variant }];
  emit();
  return id;
}

export const toast = {
  success: (options: ToastOptions) => push("success", options),
  error: (options: ToastOptions) => push("error", options),
  warning: (options: ToastOptions) => push("warning", options),
  info: (options: ToastOptions) => push("info", options),
  dismiss,
};

const variantMeta: Record<ToastVariant, { icon: React.ReactNode; accent: string }> = {
  success: {
    icon: <CheckCircle2 className="size-4 shrink-0 text-success" />,
    accent: "border-l-success",
  },
  error: {
    icon: <AlertCircle className="size-4 shrink-0 text-danger" />,
    accent: "border-l-danger",
  },
  warning: {
    icon: <TriangleAlert className="size-4 shrink-0 text-warning" />,
    accent: "border-l-warning",
  },
  info: {
    icon: <Info className="size-4 shrink-0 text-info" />,
    accent: "border-l-info",
  },
};

function ToastItem({ record }: { record: ToastRecord }) {
  const [paused, setPaused] = useState(false);
  const remainingRef = useRef(record.duration ?? DEFAULT_DURATION);
  const startedRef = useRef(Date.now());

  // 悬停时暂停计时，让用户有时间读完长文案再自动消失。
  useEffect(() => {
    if (paused) return;
    startedRef.current = Date.now();
    const timer = window.setTimeout(() => dismiss(record.id), remainingRef.current);
    return () => {
      window.clearTimeout(timer);
      remainingRef.current -= Date.now() - startedRef.current;
    };
  }, [paused, record.id]);

  return (
    <div
      className={cn(
        "animate-toast-in pointer-events-auto flex w-80 items-start gap-2.5",
        "rounded-card border border-l-3 border-line bg-surface p-3 shadow-overlay",
        variantMeta[record.variant].accent,
      )}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
    >
      <span className="mt-0.5">{variantMeta[record.variant].icon}</span>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-fg">{record.message}</div>
        {record.description && (
          <div className="mt-0.5 text-xs break-words text-fg-muted">{record.description}</div>
        )}
      </div>
      <button
        type="button"
        aria-label="关闭提示"
        onClick={() => dismiss(record.id)}
        className="shrink-0 cursor-pointer rounded p-0.5 text-fg-subtle transition-colors hover:bg-surface-hover hover:text-fg-body"
      >
        <X className="size-3.5" />
      </button>
    </div>
  );
}

// 挂在 App 顶层一次即可。用 portal 到 body，避免被页面的 overflow 裁掉。
export function Toaster() {
  const [items, setItems] = useState<ToastRecord[]>(toasts);

  useEffect(() => {
    listeners.add(setItems);
    return () => {
      listeners.delete(setItems);
    };
  }, []);

  if (typeof document === "undefined") return null;

  return createPortal(
    <div
      className="pointer-events-none fixed top-4 right-4 z-[1300] flex flex-col items-end gap-2"
      role="region"
      aria-label="通知"
    >
      {/* aria-live 容器常驻，内容变化才会被播报；
          若整个容器随通知一起挂载/卸载，读屏可能读不到。 */}
      <div aria-live="polite" aria-atomic="false" className="flex flex-col items-end gap-2">
        {items.map((record) => (
          <ToastItem key={record.id} record={record} />
        ))}
      </div>
    </div>,
    document.body,
  );
}
