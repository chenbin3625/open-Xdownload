import React, { useEffect, useState } from "react";
import { toast } from "../../lib/toast";

export function ShellDrawer({
  children,
  onClose,
  open,
  size = 760,
  title,
}: {
  children: React.ReactNode;
  onClose: () => void;
  open: boolean;
  size?: number;
  title: React.ReactNode;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    window.addEventListener("keydown", onKey);
    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener("keydown", onKey);
    };
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className="shell-drawer" role="dialog" aria-modal="true">
      <button type="button" className="shell-drawer-mask" aria-label="关闭" onClick={onClose} />
      <aside className="shell-drawer-panel" style={{ width: `min(${size}px, 100vw)` }}>
        <header className="shell-drawer-header">
          <div className="shell-drawer-title">{title}</div>
          <button type="button" className="toolbar-icon-btn" aria-label="关闭" onClick={onClose}>
            ×
          </button>
        </header>
        <div className="shell-drawer-body">{children}</div>
      </aside>
    </div>
  );
}
export function ShellPagination({
  current,
  itemName,
  onChange,
  pageSize,
  pageSizeOptions,
  total,
}: {
  current: number;
  itemName: string;
  onChange: (page: number, pageSize: number) => void;
  pageSize: number;
  pageSizeOptions: number[];
  total: number;
}) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize) || 1);
  const start = total === 0 ? 0 : (current - 1) * pageSize + 1;
  const end = Math.min(current * pageSize, total);
  return (
    <div className="shell-pagination">
      <span>
        {total > 0 ? `共 ${total} ${itemName}，当前 ${start}-${end}` : `共 0 ${itemName}`}
      </span>
      <label className="shell-pagination-size">
        <span className="visually-hidden">每页条数</span>
        <select
          value={pageSize}
          disabled={total === 0}
          onChange={(event) => onChange(1, Number(event.target.value))}
        >
          {pageSizeOptions.map((option) => (
            <option key={option} value={option}>
              {option}/页
            </option>
          ))}
        </select>
      </label>
      <button
        type="button"
        className="shell-page-btn"
        disabled={current <= 1 || total === 0}
        onClick={() => onChange(current - 1, pageSize)}
      >
        上一页
      </button>
      <span>
        {Math.min(current, totalPages)}/{totalPages}
      </span>
      <button
        type="button"
        className="shell-page-btn"
        disabled={current >= totalPages || total === 0}
        onClick={() => onChange(current + 1, pageSize)}
      >
        下一页
      </button>
    </div>
  );
}

export function CopyTextButton({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="job-text-btn"
      title={label}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          toast("已复制");
          window.setTimeout(() => setCopied(false), 1200);
        } catch {
          toast("复制失败", { tone: "err" });
        }
      }}
    >
      {copied ? "已复制" : "复制"}
    </button>
  );
}
