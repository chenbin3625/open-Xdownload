import React, { useId, useMemo, useState } from "react";
import SButton from "@douyinfe/semi-ui/lib/es/button";
import STable from "@douyinfe/semi-ui/lib/es/table";
import { ChevronRight } from "lucide-react";
import { t, useTranslation } from "../../lib/i18n";

// Semi Table 体积约 200 KB，单独成文件，避免只用 Tag/Pagination 的页面把它带进首屏。
export interface TableColumn<T> { key: string; title: React.ReactNode; width?: number; align?: "left" | "right" | "center"; render: (record: T) => React.ReactNode }
export function Table<T extends object>({ columns, data, rowKey, loading, minWidth, expandedRowRender, emptyText, footer, className }: { columns: TableColumn<T>[]; data: T[]; rowKey: (record: T) => string | number; loading?: boolean; minWidth?: number; expandedRowRender?: (record: T) => React.ReactNode; emptyText?: React.ReactNode; footer?: React.ReactNode; className?: string }) {
  const { i18n } = useTranslation();
  const [expanded, setExpanded] = useState<(string | number)[]>([]);
  const id = useId();
  const semiColumns = useMemo(() => {
    const next = columns.map(col => ({ key: col.key, title: col.title, width: col.width, align: col.align, render: (_text: unknown, record: T) => col.render(record) }));
    if (expandedRowRender) next.unshift({ key: "expand", title: "", width: 56, align: "center", render: (_text: unknown, record: T) => { const key = String(rowKey(record)); const open = expanded.includes(key); return <SButton theme="borderless" type="tertiary" aria-label={t(open ? "收起详情" : "展开详情")} aria-expanded={open} aria-controls={`${id}-${key}`} icon={<ChevronRight size={16} className={open ? "rotate-90" : undefined} />} onClick={() => setExpanded(current => current.includes(key) ? current.filter(v => v !== key) : [...current, key])} />; } });
    return next;
  }, [columns, expanded, expandedRowRender, rowKey, id, i18n.resolvedLanguage]);
  return <STable<T> className={className} columns={semiColumns} dataSource={data} rowKey={record => record ? String(rowKey(record)) : ""} loading={loading} pagination={false} scroll={minWidth ? { x: minWidth } : undefined} expandedRowRender={expandedRowRender ? (record => record && expanded.includes(String(rowKey(record))) ? <div id={`${id}-${rowKey(record)}`}>{expandedRowRender(record)}</div> : null) : undefined} hideExpandedColumn expandIcon={false} expandedRowKeys={expanded} keepDOM={false} empty={emptyText ?? t("暂无数据")} footer={footer ? () => footer : undefined} size="small" />;
}
