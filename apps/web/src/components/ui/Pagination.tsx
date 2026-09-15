import { ChevronLeft, ChevronRight } from "lucide-react";
import React from "react";
import { cn } from "../../lib/cn";
import { Select } from "./Controls";

// 页码窗口：首页、末页、当前页 ±1 常驻，其余用省略号折叠。
// 导出以便单测覆盖边界（总页数 <= 7 时不折叠）。
export function buildPageItems(current: number, totalPages: number): (number | "gap")[] {
  if (totalPages <= 7) {
    return Array.from({ length: totalPages }, (_, index) => index + 1);
  }
  const pages = new Set([1, totalPages, current, current - 1, current + 1]);
  const sorted = [...pages].filter((page) => page >= 1 && page <= totalPages).sort((a, b) => a - b);

  const items: (number | "gap")[] = [];
  let previous = 0;
  for (const page of sorted) {
    if (previous && page - previous > 1) items.push("gap");
    items.push(page);
    previous = page;
  }
  return items;
}

const PAGE_SIZE_OPTIONS = [10, 20, 50, 100];

export function Pagination({
  page,
  pageSize,
  total,
  onPageChange,
  onPageSizeChange,
  totalLabel,
  className,
}: {
  page: number;
  pageSize: number;
  total: number;
  onPageChange: (page: number) => void;
  onPageSizeChange?: (pageSize: number) => void;
  totalLabel?: (total: number) => React.ReactNode;
  className?: string;
}) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const current = Math.min(Math.max(1, page), totalPages);

  const stepClass = cn(
    "inline-flex size-8 items-center justify-center rounded-control border border-line",
    "text-fg-muted transition-colors",
    "enabled:cursor-pointer enabled:hover:border-brand-400 enabled:hover:text-brand-600",
    "disabled:cursor-not-allowed disabled:opacity-40",
  );

  return (
    <nav
      aria-label="分页"
      className={cn("flex flex-wrap items-center justify-between gap-3", className)}
    >
      <p className="text-xs text-fg-muted">{totalLabel?.(total) ?? `共 ${total} 条`}</p>
      <div className="flex items-center gap-1.5">
        <button
          type="button"
          aria-label="上一页"
          disabled={current <= 1}
          onClick={() => onPageChange(current - 1)}
          className={stepClass}
        >
          <ChevronLeft className="size-4" />
        </button>
        {buildPageItems(current, totalPages).map((item, index) =>
          item === "gap" ? (
            <span
              key={`gap-${index}`}
              aria-hidden="true"
              className="inline-flex size-8 items-center justify-center text-xs text-fg-subtle"
            >
              …
            </span>
          ) : (
            <button
              key={item}
              type="button"
              aria-label={`第 ${item} 页`}
              aria-current={item === current ? "page" : undefined}
              onClick={() => onPageChange(item)}
              className={cn(
                "inline-flex size-8 cursor-pointer items-center justify-center rounded-control",
                "border text-xs font-medium transition-colors",
                item === current
                  ? "border-brand-500 bg-brand-500 text-white"
                  : "border-line text-fg-body hover:border-brand-400 hover:text-brand-600",
              )}
            >
              {item}
            </button>
          ),
        )}
        <button
          type="button"
          aria-label="下一页"
          disabled={current >= totalPages}
          onClick={() => onPageChange(current + 1)}
          className={stepClass}
        >
          <ChevronRight className="size-4" />
        </button>
        {onPageSizeChange && (
          <Select
            size="sm"
            ariaLabel="每页条数"
            value={String(pageSize)}
            onChange={(next) => onPageSizeChange(Number(next))}
            options={PAGE_SIZE_OPTIONS.map((size) => ({
              value: String(size),
              label: `${size} 条/页`,
            }))}
            className="ml-1 w-[6.5rem]"
          />
        )}
      </div>
    </nav>
  );
}
