import React, { useId, useLayoutEffect, useRef } from "react";
import SAvatar from "@douyinfe/semi-ui/lib/es/avatar";
import Badge from "@douyinfe/semi-ui/lib/es/badge";
import SDescriptions from "@douyinfe/semi-ui/lib/es/descriptions";
import SDivider from "@douyinfe/semi-ui/lib/es/divider";
import SPagination from "@douyinfe/semi-ui/lib/es/pagination";
import STag from "@douyinfe/semi-ui/lib/es/tag";
import { cn } from "../../lib/cn";
import { t, useTranslation } from "../../lib/i18n";

export function Divider({ className }: { className?: string }) { return <SDivider className={className} />; }
export function Descriptions({ items, columns = 3, className }: { items: { key: string; label: React.ReactNode; children: React.ReactNode }[]; columns?: 1 | 2 | 3; className?: string }) { return <SDescriptions data={items.map(item => ({ key: item.label, value: item.children }))} row align="left" className={cn("semi-description-grid", className)} style={{ "--description-grid-columns": `repeat(${columns}, minmax(0, 1fr))` } as React.CSSProperties} />; }

export type Tone = "default" | "neutral" | "brand" | "success" | "warning" | "danger" | "info";
const colors = { default: "grey", neutral: "grey", brand: "blue", success: "green", warning: "amber", danger: "red", info: "cyan" } as const;
export function Tag({ tone = "default", size, icon, children, ...props }: React.HTMLAttributes<HTMLSpanElement> & { tone?: Tone; size?: "sm" | "md"; icon?: React.ReactNode }) { return <STag {...props as React.ComponentProps<typeof STag>} color={colors[tone]} size={size === "sm" ? "small" : "default"} prefixIcon={icon}>{children}</STag>; }
export function StatusDot({ tone = "default", pulse, className }: { tone?: Tone; pulse?: boolean; className?: string }) { return <Badge dot type={tone === "danger" ? "danger" : tone === "success" ? "success" : tone === "warning" ? "warning" : "primary"} className={cn(pulse && "animate-pulse", className)} />; }
export function CountBadge({ count, overflowCount = 99, className }: { count: number; overflowCount?: number; tone?: Tone; className?: string }) { return count > 0 ? <Badge count={count} overflowCount={overflowCount} className={className} /> : null; }
type SemiAvatarProps = React.ComponentProps<typeof SAvatar>;
export function Avatar({ children, className, color = "blue", size = "small" }: { children: React.ReactNode; className?: string; color?: SemiAvatarProps["color"]; size?: SemiAvatarProps["size"] }) { return <SAvatar size={size} color={color} className={className}>{children}</SAvatar>; }

export function Pagination({ page, pageSize, total, onPageChange, onPageSizeChange, pageSizeOptions = [10, 20, 50, 100], totalLabel, className }: { page: number; pageSize: number; total: number; onPageChange: (v: number) => void; onPageSizeChange?: (v: number) => void; pageSizeOptions?: number[]; totalLabel?: (n: number) => React.ReactNode; size?: "sm" | "md"; className?: string }) {
  const size = Number.isFinite(pageSize) && pageSize > 0 ? pageSize : 1; const count = Number.isFinite(total) && total > 0 ? total : 0;
  const root = useRef<HTMLElement>(null); const labelId = useId(); const { i18n } = useTranslation();
  const labelControlsRef = useRef<() => void>(() => {});
  useLayoutEffect(() => {
    const rootNode = root.current;
    const labelControls = () => {
    for (const item of root.current?.querySelectorAll<HTMLElement>(".semi-page-item") ?? []) {
    const label = item.classList.contains("semi-page-prev") ? t("上一页") : item.classList.contains("semi-page-next") ? t("下一页") : /^\d+$/.test(item.textContent?.trim() ?? "") ? t("第 {{value0}} 页", { value0: Number(item.textContent) }) : undefined;
      if (!label) continue;
      if (item.getAttribute("role") !== "button") item.setAttribute("role", "button");
      if (item.getAttribute("aria-label") !== label) item.setAttribute("aria-label", label);
      const tabIndex = item.getAttribute("aria-disabled") === "true" ? -1 : 0;
      if (item.tabIndex !== tabIndex) item.tabIndex = tabIndex;
      if (item.classList.contains("semi-page-item-active")) { if (item.getAttribute("aria-current") !== "page") item.setAttribute("aria-current", "page"); } else item.removeAttribute("aria-current");
    }
    const select = root.current?.querySelector('[role="combobox"]');
    if (select && select.getAttribute("aria-labelledby") !== labelId) select.setAttribute("aria-labelledby", labelId);
    };
    labelControlsRef.current = labelControls;
    labelControls();
    const observer = new MutationObserver(labelControls);
    if (rootNode) observer.observe(rootNode, { childList: true, subtree: true, attributes: true, attributeFilter: ["role", "aria-label", "aria-disabled", "class"] });
    return () => { observer.disconnect(); if (labelControlsRef.current === labelControls) labelControlsRef.current = () => {}; };
  }, [labelId, i18n.resolvedLanguage]);
  useLayoutEffect(() => { labelControlsRef.current(); }, [page, size, count]);
  return <nav ref={root} aria-label={t("分页")} className={cn("semi-pagination-bar", className)} onKeyDown={e => { if (["Enter", " "].includes(e.key) && (e.target as HTMLElement).getAttribute("role") === "button") { e.preventDefault(); (e.target as HTMLElement).click(); } }}><span>{totalLabel?.(count) ?? t("共 {{value0}} 条", { value0: count })}</span><span className="sr-only" id={labelId}>{t("每页条数")}</span><SPagination currentPage={Math.max(1, Math.min(Number.isFinite(page) ? page : 1, Math.max(1, Math.ceil(count / size))))} pageSize={size} total={Math.max(1, count)} onPageChange={onPageChange} onPageSizeChange={onPageSizeChange} showSizeChanger={!!onPageSizeChange} pageSizeOpts={pageSizeOptions} /></nav>;
}
export function buildPageItems(current: number, total: number): (number | "gap")[] { const items: (number | "gap")[] = []; let previous = 0; for (let n = 1; n <= total; n++) { if (total > 7 && n !== 1 && n !== total && Math.abs(n - current) > 1) continue; if (previous && n - previous > 1) items.push("gap"); items.push(n); previous = n; } return items; }
