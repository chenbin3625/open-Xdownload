import { t, useTranslation } from "../../lib/i18n";
import { ChevronRight } from "lucide-react";
import React, { useState } from "react";
import { cn } from "../../lib/cn";
import { Empty } from "./Feedback";
import { LoadingOverlay } from "./Spinner";

export interface TableColumn<T> {
  key: string;
  title: React.ReactNode;
  /** 列宽（px）。与 minWidth 一起决定横向滚动时的布局。 */
  width?: number;
  align?: "left" | "right" | "center";
  render: (record: T) => React.ReactNode;
}

const alignClass = {
  left: "text-left",
  right: "text-right",
  center: "text-center",
} as const;

// 手写表格。语义 <table> + 横向滚动容器（表头不吸顶：垂直滚动发生在页面/外层
// 容器上，要吸顶得给表格加 max-height，那会改变调用方布局，所以这里不做，
// 也不在注释里声称支持）。
// 展开行用第二个 <tr> 承载，colSpan 跨满整行；展开按钮带 aria-expanded/aria-controls。
export function Table<T>({
  columns,
  data,
  rowKey,
  loading = false,
  minWidth,
  expandedRowRender,
  emptyText = t("暂无数据"),
  footer,
  className,
}: {
  columns: TableColumn<T>[];
  data: T[];
  rowKey: (record: T) => string | number;
  loading?: boolean;
  minWidth?: number;
  expandedRowRender?: (record: T) => React.ReactNode;
  emptyText?: React.ReactNode;
  footer?: React.ReactNode;
  className?: string;
}) {
  useTranslation();
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const baseId = React.useId();

  function toggle(key: string) {
    setExpanded((current) => {
      const next = new Set(current);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  }

  const columnCount = columns.length + (expandedRowRender ? 1 : 0);
  const cellClass = "px-3 py-2.5 align-middle";

  return (
    <div className={cn("flex flex-col", className)}>
      <LoadingOverlay loading={loading}>
        <div className="overflow-x-auto">
          <table
            style={minWidth ? { minWidth: `${minWidth}px` } : undefined}
            className="w-full border-collapse text-sm"
          >
            <thead>
              <tr className="border-b border-line bg-surface-muted">
                {expandedRowRender && <th scope="col" className="w-9" />}
                {columns.map((column) => (
                  <th
                    key={column.key}
                    scope="col"
                    style={column.width ? { width: `${column.width}px` } : undefined}
                    className={cn(
                      "px-3 py-2.5 text-xs font-medium whitespace-nowrap text-fg-muted",
                      alignClass[column.align ?? "left"],
                    )}
                  >
                    {column.title}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.length === 0 ? (
                <tr>
                  <td colSpan={columnCount}>
                    <Empty description={emptyText} />
                  </td>
                </tr>
              ) : (
                data.map((record) => {
                  const key = String(rowKey(record));
                  const isOpen = expanded.has(key);
                  const panelId = `${baseId}-${key}`;
                  return (
                    <React.Fragment key={key}>
                      <tr className="border-b border-line transition-colors hover:bg-surface-hover">
                        {expandedRowRender && (
                          <td className="pl-2">
                            <button
                              type="button"
                              aria-expanded={isOpen}
                              aria-controls={panelId}
                              aria-label={isOpen ? t("收起详情") : t("展开详情")}
                              onClick={() => toggle(key)}
                              className={cn(
                                "flex size-7 cursor-pointer items-center justify-center rounded-control",
                                "text-fg-subtle transition-colors hover:bg-surface-hover hover:text-fg-body",
                              )}
                            >
                              <ChevronRight
                                className={cn(
                                  "size-4 transition-transform duration-150",
                                  isOpen && "rotate-90",
                                )}
                              />
                            </button>
                          </td>
                        )}
                        {columns.map((column) => (
                          <td
                            key={column.key}
                            className={cn(cellClass, alignClass[column.align ?? "left"])}
                          >
                            {column.render(record)}
                          </td>
                        ))}
                      </tr>
                      {isOpen && (
                        <tr id={panelId} className="border-b border-line bg-surface-muted">
                          <td colSpan={columnCount} className="px-4 py-3">
                            {expandedRowRender?.(record)}
                          </td>
                        </tr>
                      )}
                    </React.Fragment>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
      </LoadingOverlay>
      {footer && <div className="border-t border-line px-3 py-2.5">{footer}</div>}
    </div>
  );
}
