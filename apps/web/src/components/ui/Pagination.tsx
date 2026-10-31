import { t, useTranslation } from "../../lib/i18n";
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
  pageSizeOptions = PAGE_SIZE_OPTIONS,
  total,
  onPageChange,
  onPageSizeChange,
  totalLabel,
  size = "md",
  className,
}: {
  page: number;
  pageSize: number;
  pageSizeOptions?: number[];
  total: number;
  onPageChange: (page: number) => void;
  onPageSizeChange?: (pageSize: number) => void;
  totalLabel?: (total: number) => React.ReactNode;
  size?: "sm" | "md";
  className?: string;
}) {
  useTranslation();
  // 边界：pageSize<=0 会让 Math.ceil(total/0) 变成 Infinity（渲染出 "Infinity" 页码），
  // total/page 非有限值时同理。这里统一收敛成"至少 1 页"，避免任何越界页码。
  const safePageSize = Number.isFinite(pageSize) && pageSize > 0 ? pageSize : 1;
  const safeTotal = Number.isFinite(total) && total > 0 ? total : 0;
  const totalPages = Math.max(1, Math.ceil(safeTotal / safePageSize));
  const safePage = Number.isFinite(page) ? page : 1;
  const current = Math.min(Math.max(1, Math.trunc(safePage)), totalPages);

  const isSmall = size === "sm";
  const itemSizeClass = isSmall ? "size-11 text-xs sm:size-7" : "size-11 text-xs sm:size-8";
  const iconSizeClass = isSmall ? "size-3.5" : "size-4";

  const stepClass = cn(
    "inline-flex items-center justify-center rounded-control border border-line",
    "text-fg-muted transition-colors",
    "enabled:cursor-pointer enabled:hover:border-brand-400 enabled:hover:text-brand-600",
    "disabled:cursor-not-allowed disabled:opacity-40",
    itemSizeClass,
  );

  return (
    <nav
      aria-label={t("分页")}
      className={cn("grid min-w-0 grid-cols-[1fr_auto] items-center gap-3 sm:flex sm:flex-wrap sm:justify-between", className)}
    >
      <p className="text-xs text-fg-muted">{totalLabel?.(safeTotal) ?? t("共 {{value0}} 条", { value0: safeTotal })}</p>
      <div className="col-span-2 row-start-2 flex items-center justify-center gap-1.5">
        <button
          type="button"
          aria-label={t("上一页")}
          disabled={current <= 1}
          onClick={() => onPageChange(current - 1)}
          className={stepClass}
        >
          <ChevronLeft className={iconSizeClass} />
        </button>
        <span className="min-w-16 text-center font-mono text-xs text-fg-body sm:hidden" aria-live="polite">
          {current} / {totalPages}
        </span>
        <div className="hidden items-center gap-1.5 sm:flex">
          {buildPageItems(current, totalPages).map((item, index) =>
            item === "gap" ? (
              <span
                key={`gap-${index}`}
                aria-hidden="true"
                className={cn("inline-flex items-center justify-center text-fg-subtle select-none", itemSizeClass)}
              >
                …
              </span>
            ) : (
              <button
                key={item}
                type="button"
                aria-label={t("第 {{value0}} 页", { value0: item })}
                aria-current={item === current ? "page" : undefined}
                onClick={() => onPageChange(item)}
                className={cn(
                  "inline-flex cursor-pointer items-center justify-center rounded-control",
                  "border font-medium transition-colors leading-none",
                  itemSizeClass,
                  item === current
                    ? "border-brand-500 bg-brand-500 text-brand-contrast"
                    : "border-line text-fg-body hover:border-brand-400 hover:text-brand-600",
                )}
              >
                {item}
              </button>
            ),
          )}
        </div>
        <button
          type="button"
          aria-label={t("下一页")}
          disabled={current >= totalPages}
          onClick={() => onPageChange(current + 1)}
          className={stepClass}
        >
          <ChevronRight className={iconSizeClass} />
        </button>
      </div>
      {onPageSizeChange && (
        <Select
          size="sm"
          ariaLabel={t("每页条数")}
          value={String(pageSize)}
          onChange={(next) => onPageSizeChange(Number(next))}
          options={pageSizeOptions.map((optSize) => ({
            value: String(optSize),
            label: t("{{value0}} 条/页", { value0: optSize }),
          }))}
          className={cn("w-[6.5rem]", !isSmall && "sm:h-8 sm:text-xs")}
        />
      )}
    </nav>
  );
}
