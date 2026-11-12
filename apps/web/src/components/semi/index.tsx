import "@douyinfe/semi-ui/react19-adapter";
import React, { createContext, forwardRef, useContext, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import {
  Avatar as SAvatar, Badge, Banner, Button as SButton,
  Checkbox as SCheckbox, ConfigProvider, Descriptions as SDescriptions,
  Divider as SDivider, Empty as SEmpty, Form, Input as SInput, InputNumber,
  Modal as SModal, Pagination as SPagination, Popconfirm as SPopconfirm,
  Popover, Progress as SProgress, Select as SSelect, SideSheet,
  Skeleton as SSkeleton, Spin, Switch as SSwitch, Table as STable,
  Tabs as STabs, Tag as STag, TextArea, Toast, Tooltip as STooltip, Tree as STree,
} from "@douyinfe/semi-ui";
import zhCN from "@douyinfe/semi-ui/lib/es/locale/source/zh_CN";
import enUS from "@douyinfe/semi-ui/lib/es/locale/source/en_US";
import { ChevronRight, Eye, EyeOff, X } from "lucide-react";
import { cn } from "../../lib/cn";
import { t, useTranslation } from "../../lib/i18n";

export function TooltipProvider({ children }: { children: React.ReactNode }) {
  const { i18n } = useTranslation();
  return <ConfigProvider locale={i18n.language.startsWith("en") ? enUS : zhCN}>{children}</ConfigProvider>;
}

const FieldContext = createContext<string | undefined>(undefined);
export function useFieldId() { return useContext(FieldContext); }
export function Field({ label, tooltip, htmlFor, children, className }: { label: React.ReactNode; tooltip?: React.ReactNode; htmlFor?: string; children: React.ReactNode; className?: string }) {
  const generated = useId(); const id = htmlFor ?? generated;
  return <FieldContext.Provider value={id}><div className={cn("semi-field", className)}><div className="flex items-center gap-1"><Form.Label id={`${id}-label`} name={id} text={label} />{tooltip}</div>{children}</div></FieldContext.Provider>;
}
export interface InputProps extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "size" | "prefix"> {
  size?: "sm" | "md"; prefix?: React.ReactNode; suffix?: React.ReactNode;
  allowClear?: boolean; onPressEnter?: (e: React.KeyboardEvent<HTMLInputElement>) => void;
  onClear?: () => void; containerClassName?: string;
  mode?: "password";
}
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input({ size, onChange, onPressEnter, containerClassName, className, id, allowClear, onClear, mode, suffix, ...props }, ref) {
  const fieldId = useFieldId();
  const [visible, setVisible] = useState(false);
  const [internalValue, setInternalValue] = useState(props.defaultValue ?? "");
  const value = props.value ?? internalValue;
  const generatedId = useId(); const inputId = id ?? fieldId ?? generatedId;
  const actions = <>{allowClear && value !== "" && !props.disabled && <SButton theme="borderless" type="tertiary" size="small" icon={<X size={14} />} aria-label={t("清空")} onClick={() => { setInternalValue(""); onClear?.(); onChange?.({ target: { value: "" } } as React.ChangeEvent<HTMLInputElement>); }} />}{mode === "password" && <SButton theme="borderless" type="tertiary" size="small" icon={visible ? <EyeOff size={14} /> : <Eye size={14} />} aria-label={t(visible ? "隐藏" : "显示")} aria-pressed={visible} onClick={() => setVisible(v => !v)} />}{suffix}</>;
  return <SInput {...props as React.ComponentProps<typeof SInput>} value={value as string | number} id={inputId} type={mode === "password" ? visible ? "text" : "password" : props.type} size={size === "sm" ? "small" : "default"} className={cn(containerClassName, className)} suffix={allowClear || mode || suffix ? actions : undefined} onChange={(v, event) => { setInternalValue(v); onChange?.(event); }} onEnterPress={e => { if (!e.nativeEvent.isComposing) onPressEnter?.(e); }} ref={ref} />;
});
export const PasswordInput = forwardRef<HTMLInputElement, InputProps>((props, ref) => <Input {...props} ref={ref} mode="password" />);
export interface TextareaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> { containerClassName?: string }
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea({ onChange, containerClassName, className, id, ...props }, ref) {
  const fieldId = useFieldId();
  return <TextArea {...props as React.ComponentProps<typeof TextArea>} id={id ?? fieldId} className={cn(containerClassName, className)} onChange={(_, event) => onChange?.(event as unknown as React.ChangeEvent<HTMLTextAreaElement>)} ref={ref} />;
});
export interface NumberInputProps extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "size" | "value" | "onChange"> { value: number | null; onChange: (v: number | null) => void; size?: "sm" | "md"; addonAfter?: React.ReactNode }
export const NumberInput = forwardRef<HTMLInputElement, NumberInputProps>(function NumberInput({ value, onChange, size, addonAfter, id, ...props }, ref) {
  const fieldId = useFieldId(); return <InputNumber {...props as React.ComponentProps<typeof InputNumber>} id={id ?? fieldId} value={value ?? ""} forwardedRef={ref as React.ComponentProps<typeof InputNumber>["forwardedRef"]} onChange={v => onChange(v === "" ? null : Number(v))} size={size === "sm" ? "small" : "default"} suffix={addonAfter} />;
});
export interface SelectOption { value: string; label: string }
interface SelectProps { value: string; onChange: (value: string) => void; options: SelectOption[]; placeholder?: string; size?: "sm" | "md"; className?: string; ariaLabel?: string; id?: string; disabled?: boolean; motion?: boolean }
export function Select({ onChange, options, size, ariaLabel, id, ...props }: SelectProps) {
  const fieldId = useFieldId(); const labelId = useId();
  return <>{ariaLabel && <span id={labelId} className="sr-only">{ariaLabel}</span>}<SSelect {...props} id={id ?? fieldId} aria-labelledby={ariaLabel ? labelId : fieldId ? `${fieldId}-label` : undefined} optionList={options.map(option => ({ ...option, showTick: false }))} onChange={value => onChange(String(value))} size={size === "sm" ? "small" : "default"} /></>;
}
export function SearchSelect({ searchPlaceholder, onChange, options, size, ariaLabel, ...props }: SelectProps & { searchPlaceholder?: string }) {
  const fieldId = useFieldId(); const labelId = useId();
  const select = useRef<SSelect>(null); const [open, setOpen] = useState(false);
  const listId = `${labelId}-listbox`;
  const searchLabel = [ariaLabel, searchPlaceholder ?? t("搜索...")].filter(Boolean).join(" ");
  useLayoutEffect(() => {
    if (!open) return;
    const observer = new MutationObserver(labelList);
    function labelList() {
      const list = select.current?.optionContainerEl.current?.querySelector('[role="listbox"]');
      if (list) { list.id = listId; select.current?.triggerRef.current?.setAttribute("aria-controls", listId); observer.disconnect(); }
    }
    observer.observe(document.body, { childList: true, subtree: true });
    labelList();
    return () => observer.disconnect();
  }, [open, listId]);
  return <>{ariaLabel && <span id={labelId} className="sr-only">{ariaLabel}</span>}<SSelect {...props} ref={select} id={props.id ?? fieldId} aria-labelledby={ariaLabel ? labelId : fieldId ? `${fieldId}-label` : undefined} filter searchPosition="dropdown" onDropdownVisibleChange={setOpen} inputProps={{ autoFocus: true, role: "combobox", "aria-label": searchLabel, "aria-expanded": open, "aria-controls": listId, "aria-autocomplete": "list", onKeyDownCapture: event => {
    if (event.key === "Escape" && !event.nativeEvent.isComposing) {
      event.preventDefault(); event.stopPropagation();
      select.current?.close(); select.current?.triggerRef.current?.focus();
    }
  } }} searchPlaceholder={searchPlaceholder ?? t("搜索...")} emptyContent={t("无匹配项")} optionList={options.map(option => ({ ...option, showTick: false }))} onChange={value => onChange(String(value))} size={size === "sm" ? "small" : "default"} /></>;
}
export function Switch({ ariaLabel, onChange, ...props }: { checked: boolean; onChange: (v: boolean) => void; loading?: boolean; disabled?: boolean; ariaLabel?: string }) { return <SSwitch {...props} disabled={props.disabled || props.loading} aria-label={ariaLabel} onChange={checked => onChange(checked)} />; }
export function Checkbox({ onChange, label, ariaLabel, ...props }: { checked: boolean; onChange: (v: boolean) => void; label?: React.ReactNode; ariaLabel?: string; id?: string }) { return <SCheckbox {...props} aria-label={ariaLabel} onChange={e => onChange(e.target.checked === true)}>{label}</SCheckbox>; }
export interface TabItem { key: string; label: React.ReactNode; children?: React.ReactNode }
export function Tabs({ value, onChange, items, className }: { value: string; onChange: (v: string) => void; items: TabItem[]; className?: string }) { return <STabs activeKey={value} onChange={onChange} className={className}>{items.map(item => <STabs.TabPane itemKey={item.key} tab={item.label} key={item.key}>{item.key === value && item.children}</STabs.TabPane>)}</STabs>; }

interface OverlayProps { open: boolean; onClose: () => void; title?: React.ReactNode; children?: React.ReactNode; className?: string }
export function Modal({ open, onClose, description, footer, bodyClassName, children, ...props }: OverlayProps & { description?: React.ReactNode; footer?: React.ReactNode; bodyClassName?: string }) {
  return <SModal {...props} visible={open} onCancel={onClose} footer={footer ?? null} width="min(94vw, 672px)" closeOnEsc motion={false} keepDOM={false} maskClosable={false}><div className={bodyClassName} onKeyDownCapture={event => {
    // Semi's select popover consumes Escape even while its options are closed.
    const target = event.target as HTMLElement;
    if (event.key === "Escape" && !event.nativeEvent.isComposing && target.closest('[role="combobox"][aria-expanded="false"]') && target.closest('[role="dialog"]') === event.currentTarget.closest('[role="dialog"]')) {
      event.preventDefault(); event.stopPropagation(); onClose();
    }
  }}>{description && <p className="mb-4 text-fg-muted">{description}</p>}{children}</div></SModal>;
}
export function MediaModal({ open, onClose, ...props }: OverlayProps) { return <SModal {...props} visible={open} onCancel={onClose} footer={null} width="min(96vw, 1088px)" closeOnEsc motion={false} keepDOM={false} />; }
export function Drawer({ open, onClose, side = "right", width = "28rem", title, ...props }: OverlayProps & { side?: "left" | "right"; width?: string }) {
  const id = useId(); const heading = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return;
    const dialog = heading.current?.closest<HTMLElement>('[role="dialog"]');
    if (!dialog) return;
    dialog.setAttribute("aria-labelledby", id); dialog.setAttribute("aria-modal", "true"); dialog.tabIndex = -1;
    const previous = document.activeElement as HTMLElement | null;
    const focusable = () => Array.from(dialog.querySelectorAll<HTMLElement>('button:not([disabled]), a[href], input:not([disabled]), textarea:not([disabled]), [tabindex="0"]')).filter(item => item.getAttribute("aria-hidden") !== "true");
    (focusable()[0] ?? dialog).focus();
    function trap(event: KeyboardEvent) {
      if (event.key !== "Tab" || !dialog || !dialog.contains(document.activeElement)) return;
      const items = focusable(); const index = items.indexOf(document.activeElement as HTMLElement);
      if (!items.length || (event.shiftKey ? index <= 0 : index === items.length - 1)) { event.preventDefault(); (event.shiftKey ? items.at(-1) : items[0] ?? dialog)?.focus(); }
    }
    document.addEventListener("keydown", trap);
    return () => { document.removeEventListener("keydown", trap); if (previous?.isConnected) previous.focus(); };
  }, [open, id]);
  return <SideSheet {...props} title={<span ref={heading} id={id} className={title ? undefined : "sr-only"}>{title ?? t("详情")}</span>} visible={open} onCancel={onClose} placement={side} width={`min(100vw, ${width})`} closeOnEsc motion={false} disableScroll keepDOM={false} />;
}
export function Tooltip({ title, content, children, side = "top" }: { title?: React.ReactNode; content?: React.ReactNode; children: React.ReactNode; side?: "top" | "right" | "bottom" | "left" }) { return <STooltip content={title ?? content} position={side} zIndex={1400}>{children}</STooltip>; }
export function Popconfirm({ title, description, okText, cancelText, danger, disabled, children, onConfirm }: { title: React.ReactNode; description?: React.ReactNode; okText?: string; cancelText?: string; danger?: boolean; disabled?: boolean; children: React.ReactNode; onConfirm: () => void }) {
  const id = useId(); const [visible, setVisible] = useState(false);
  if (disabled) return <>{children}</>;
  const trigger = React.isValidElement<{ onClick?: React.MouseEventHandler }>(children) ? React.cloneElement(children, { onClick: event => { children.props.onClick?.(event); setVisible(current => !current); } }) : <span onClick={() => setVisible(true)}>{children}</span>;
  return <SPopconfirm visible={visible} onVisibleChange={setVisible} onClickOutSide={() => setVisible(false)} title={<span id={`${id}-title`} ref={node => { const dialog = node?.closest('[role="alertdialog"], [role="dialog"]'); dialog?.setAttribute("role", "alertdialog"); dialog?.setAttribute("aria-labelledby", `${id}-title`); if (description) dialog?.setAttribute("aria-describedby", `${id}-description`); }}>{title}</span>} content={description ? <span id={`${id}-description`}>{description}</span> : undefined} okText={okText ?? t("确定")} cancelText={cancelText ?? t("取消")} okType={danger ? "danger" : "primary"} onConfirm={() => { onConfirm(); setVisible(false); }} onCancel={() => setVisible(false)} motion={false} keepDOM={false} zIndex={1350}>{trigger}</SPopconfirm>;
}
export function SimplePopover({ trigger, children, side = "bottom", className }: { trigger: React.ReactNode; children: React.ReactNode; side?: "top" | "right" | "bottom" | "left"; align?: "start" | "center" | "end"; className?: string }) { return <Popover trigger="click" position={side} content={<div className={cn("p-3", className)}>{children}</div>} zIndex={1350}>{trigger}</Popover>; }

export function Spinner({ className }: { className?: string }) { return <Spin size="small" wrapperClassName={className} />; }
export function LoadingOverlay({ loading, children, tip }: { loading: boolean; children: React.ReactNode; tip?: string }) { return <Spin spinning={loading} tip={tip} wrapperClassName="w-full min-w-0">{children}</Spin>; }
export function Alert({ type = "info", message, description, action, showIcon = true, className }: { type?: "info" | "success" | "warning" | "error"; message: React.ReactNode; description?: React.ReactNode; action?: React.ReactNode; showIcon?: boolean; className?: string }) { return <div role={type === "error" ? "alert" : "status"}><Banner type={type === "error" ? "danger" : type} title={message} description={description} closeIcon={null} icon={showIcon ? undefined : null} className={className}>{action}</Banner></div>; }
export function Skeleton({ className }: { className?: string }) { return <SSkeleton loading active placeholder={<SSkeleton.Paragraph rows={1} />} className={className} />; }
export function ListSkeleton({ rows = 4, avatar = true }: { rows?: number; avatar?: boolean }) { return <div role="status" aria-label={t("加载中")} className="space-y-4">{Array.from({ length: rows }, (_, i) => <SSkeleton key={i} loading active placeholder={<div className="flex gap-3">{avatar && <SSkeleton.Avatar />}<SSkeleton.Paragraph rows={2} style={{ width: "100%" }} /></div>} />)}</div>; }
export function Empty({ description, children, className }: { description: React.ReactNode; children?: React.ReactNode; className?: string }) { return <SEmpty description={description} className={cn("py-8", className)}>{children}</SEmpty>; }
export function Progress({ percent, status, className }: { percent: number; status?: "normal" | "active" | "success" | "exception"; className?: string }) { return <SProgress percent={Math.max(0, Math.min(100, percent))} showInfo={false} stroke={status === "exception" ? "var(--semi-color-danger)" : status === "success" ? "var(--semi-color-success)" : undefined} className={className} />; }
export function Divider({ className }: { className?: string }) { return <SDivider className={className} />; }
export function Descriptions({ items, columns = 3, className }: { items: { key: string; label: React.ReactNode; children: React.ReactNode }[]; columns?: 1 | 2 | 3; className?: string }) { return <SDescriptions data={items.map(item => ({ key: item.label, value: item.children }))} row align="left" className={cn("semi-description-grid", className)} style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }} />; }

export type Tone = "default" | "neutral" | "brand" | "success" | "warning" | "danger" | "info";
const colors = { default: "grey", neutral: "grey", brand: "blue", success: "green", warning: "amber", danger: "red", info: "cyan" } as const;
export function Tag({ tone = "default", size, icon, children, ...props }: React.HTMLAttributes<HTMLSpanElement> & { tone?: Tone; size?: "sm" | "md"; icon?: React.ReactNode }) { return <STag {...props as React.ComponentProps<typeof STag>} color={colors[tone]} size={size === "sm" ? "small" : "default"} prefixIcon={icon}>{children}</STag>; }
export function StatusDot({ tone = "default", pulse, className }: { tone?: Tone; pulse?: boolean; className?: string }) { return <Badge dot type={tone === "danger" ? "danger" : tone === "success" ? "success" : tone === "warning" ? "warning" : "primary"} className={cn(pulse && "animate-pulse", className)} />; }
export function CountBadge({ count, overflowCount = 99, className }: { count: number; overflowCount?: number; tone?: Tone; className?: string }) { return count > 0 ? <Badge count={count} overflowCount={overflowCount} className={className} /> : null; }
export function Avatar({ children, className }: { children: React.ReactNode; className?: string }) { return <SAvatar size="small" color="blue" className={className}>{children}</SAvatar>; }

export interface TableColumn<T> { key: string; title: React.ReactNode; width?: number; align?: "left" | "right" | "center"; render: (record: T) => React.ReactNode }
export function Table<T extends object>({ columns, data, rowKey, loading, minWidth, expandedRowRender, emptyText, footer, className }: { columns: TableColumn<T>[]; data: T[]; rowKey: (record: T) => string | number; loading?: boolean; minWidth?: number; expandedRowRender?: (record: T) => React.ReactNode; emptyText?: React.ReactNode; footer?: React.ReactNode; className?: string }) {
  const [expanded, setExpanded] = useState<(string | number)[]>([]);
  const id = useId();
  const semiColumns = columns.map(col => ({ key: col.key, title: col.title, width: col.width, align: col.align, render: (_text: unknown, record: T) => col.render(record) }));
  if (expandedRowRender) semiColumns.unshift({ key: "expand", title: "", width: 56, align: "center", render: (_text: unknown, record: T) => { const key = String(rowKey(record)); const open = expanded.includes(key); return <SButton theme="borderless" type="tertiary" aria-label={t(open ? "收起详情" : "展开详情")} aria-expanded={open} aria-controls={`${id}-${key}`} icon={<ChevronRight size={16} className={open ? "rotate-90" : undefined} />} onClick={() => setExpanded(current => open ? current.filter(v => v !== key) : [...current, key])} />; } });
  return <STable<T> className={className} columns={semiColumns} dataSource={data} rowKey={record => record ? String(rowKey(record)) : ""} loading={loading} pagination={false} scroll={minWidth ? { x: minWidth } : undefined} expandedRowRender={expandedRowRender ? (record => record && expanded.includes(String(rowKey(record))) ? <div id={`${id}-${rowKey(record)}`}>{expandedRowRender(record)}</div> : null) : undefined} hideExpandedColumn expandIcon={false} expandedRowKeys={expanded} keepDOM={false} empty={emptyText ?? t("暂无数据")} footer={footer ? () => footer : undefined} size="small" />;
}
export function Pagination({ page, pageSize, total, onPageChange, onPageSizeChange, pageSizeOptions = [10, 20, 50, 100], totalLabel, className }: { page: number; pageSize: number; total: number; onPageChange: (v: number) => void; onPageSizeChange?: (v: number) => void; pageSizeOptions?: number[]; totalLabel?: (n: number) => React.ReactNode; size?: "sm" | "md"; className?: string }) {
  const size = Number.isFinite(pageSize) && pageSize > 0 ? pageSize : 1; const count = Number.isFinite(total) && total > 0 ? total : 0;
  const root = useRef<HTMLElement>(null); const labelId = useId(); useTranslation();
  useLayoutEffect(() => {
    function labelControls() {
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
    }
    labelControls();
    const observer = new MutationObserver(labelControls);
    if (root.current) observer.observe(root.current, { childList: true, subtree: true, attributes: true, attributeFilter: ["role", "aria-label", "aria-disabled", "class"] });
    return () => observer.disconnect();
  });
  return <nav ref={root} aria-label={t("分页")} className={cn("semi-pagination-bar", className)} onKeyDown={e => { if (["Enter", " "].includes(e.key) && (e.target as HTMLElement).getAttribute("role") === "button") { e.preventDefault(); (e.target as HTMLElement).click(); } }}><span>{totalLabel?.(count) ?? t("共 {{value0}} 条", { value0: count })}</span><span className="sr-only" id={labelId}>{t("每页条数")}</span><SPagination currentPage={Math.max(1, Math.min(Number.isFinite(page) ? page : 1, Math.max(1, Math.ceil(count / size))))} pageSize={size} total={Math.max(1, count)} onPageChange={onPageChange} onPageSizeChange={onPageSizeChange} showSizeChanger={!!onPageSizeChange} pageSizeOpts={pageSizeOptions} /></nav>;
}
export function buildPageItems(current: number, total: number): (number | "gap")[] { const items: (number | "gap")[] = []; let previous = 0; for (let n = 1; n <= total; n++) { if (total > 7 && n !== 1 && n !== total && Math.abs(n - current) > 1) continue; if (previous && n - previous > 1) items.push("gap"); items.push(n); previous = n; } return items; }
export interface TreeNode { key: string; title: React.ReactNode; isLeaf?: boolean; children?: TreeNode[] }
export function Tree({ nodes, expandedKeys, selectedKey, onExpandedChange, onSelect, loadChildren, onLoadError, className }: { nodes: TreeNode[]; expandedKeys: string[]; selectedKey?: string; onExpandedChange: (keys: string[]) => void; onSelect: (node: TreeNode) => void; loadChildren?: (node: TreeNode) => void | Promise<void>; onLoadError?: (node: TreeNode, e: unknown) => void; className?: string }) {
  const root = useRef<HTMLDivElement>(null); const [focused, setFocused] = useState<string>();
  const [loading, setLoading] = useState<string[]>([]);
  const visible: TreeNode[] = []; const parents = new Map<string, string>();
  function visit(items: TreeNode[], parent?: string) { for (const item of items) { visible.push(item); if (parent) parents.set(item.key, parent); if (expandedKeys.includes(item.key)) visit(item.children ?? [], item.key); } }
  visit(nodes);
  useLayoutEffect(() => {
    root.current?.querySelector('[role="tree"]')?.setAttribute("aria-label", t("目录树"));
    for (const item of root.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? []) {
      const node = visible.find(node => node.key === item.dataset.key);
      item.tabIndex = node?.key === (visible.some(node => node.key === focused) ? focused : visible[0]?.key) ? 0 : -1;
      if (node?.isLeaf) item.removeAttribute("aria-expanded");
    }
  });
  async function expand(node: TreeNode, open: boolean) {
    if (open && loadChildren && !node.children && !node.isLeaf) {
      if (loading.includes(node.key)) return;
      setLoading(current => [...current, node.key]);
      try { await loadChildren(node); } catch (error) { onLoadError?.(node, error); return; }
      finally { setLoading(current => current.filter(key => key !== node.key)); }
    }
    onExpandedChange(open ? [...new Set([...expandedKeys, node.key])] : expandedKeys.filter(key => key !== node.key));
  }
  function treeData(items: TreeNode[]): TreeNode[] { return items.map(node => ({ ...node, isLeaf: node.isLeaf ?? (node.children ? node.children.length === 0 : !loadChildren), children: node.children ? treeData(node.children) : undefined })); }
  return <div ref={root} onFocus={e => { const item = (e.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]'); if (item) setFocused(item.dataset.key); }} onKeyDown={e => {
    const item = (e.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]'); const index = visible.findIndex(node => node.key === item?.dataset.key); if (index < 0) return;
    const node = visible[index]; let next: string | undefined;
    if (e.key === "ArrowDown") next = visible[Math.min(index + 1, visible.length - 1)]?.key;
    else if (e.key === "ArrowUp") next = visible[Math.max(index - 1, 0)]?.key;
    else if (e.key === "Home") next = visible[0]?.key;
    else if (e.key === "End") next = visible.at(-1)?.key;
    else if (e.key === "ArrowRight" && !node.isLeaf) { if (expandedKeys.includes(node.key)) next = node.children?.[0]?.key; else void expand(node, true); }
    else if (e.key === "ArrowLeft") { if (expandedKeys.includes(node.key)) void expand(node, false); else next = parents.get(node.key); }
    else if (e.key === "Enter" || e.key === " ") onSelect(node);
    else return;
    e.preventDefault(); e.stopPropagation();
    if (next) { setFocused(next); Array.from(root.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? []).find(item => item.dataset.key === next)?.focus(); }
  }}><STree treeData={treeData(nodes)} keyMaps={{ label: "title", value: "key" }} loadData={loadChildren ? async () => {} : undefined} expandAction="doubleClick" expandedKeys={expandedKeys} value={selectedKey} onExpand={(_keys, data) => { const node = data.node as TreeNode; void expand(node, data.expanded === true); }} onSelect={(_keys, _selected, node) => onSelect(node as TreeNode)} renderLabel={(label, node) => <span>{label}{loading.includes(String(node?.key)) && <Spin size="small" />}</span>} className={className} motion={false} /></div>;
}
export interface ToastOptions { message: React.ReactNode; description?: React.ReactNode; duration?: number }
let toastSequence = 0;
const toastIds = new Map<number, string>();
function ToastMessage({ id, options }: { id: number; options: ToastOptions }) {
  const [focused, setFocused] = useState(false); const [hovered, setHovered] = useState(false);
  const duration = options.duration ?? 4500;
  useEffect(() => { if (focused || hovered || duration <= 0) return; const timer = setTimeout(() => toast.dismiss(id), duration); return () => clearTimeout(timer); }, [id, duration, focused, hovered]);
  return <div className="flex items-start gap-3" onFocusCapture={() => setFocused(true)} onBlurCapture={e => { if (!e.currentTarget.contains(e.relatedTarget)) setFocused(false); }} onMouseEnter={() => setHovered(true)} onMouseLeave={() => setHovered(false)}><div>{options.message}{options.description && <div className="mt-1 text-xs">{options.description}</div>}</div><SButton size="small" theme="borderless" type="tertiary" icon={<X size={14} />} aria-label={t("关闭提示")} onClick={() => toast.dismiss(id)} /></div>;
}
function notify(type: "success" | "error" | "warning" | "info", options: ToastOptions) { const id = ++toastSequence; toastIds.set(id, Toast[type]({ content: <ToastMessage id={id} options={options} />, duration: 0, showClose: false, onClose: () => toastIds.delete(id) })); return id; }
export const toast = { success: (o: ToastOptions) => notify("success", o), error: (o: ToastOptions) => notify("error", o), warning: (o: ToastOptions) => notify("warning", o), info: (o: ToastOptions) => notify("info", o), dismiss: (id: number) => { const key = toastIds.get(id); if (key) Toast.close(key); toastIds.delete(id); } };
export function Toaster() { return null; }
