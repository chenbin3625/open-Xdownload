import React, { useEffect, useId, useRef, useState } from "react";
import SModal from "@douyinfe/semi-ui/lib/es/modal";
import SPopconfirm from "@douyinfe/semi-ui/lib/es/popconfirm";
import Popover from "@douyinfe/semi-ui/lib/es/popover";
import SideSheet from "@douyinfe/semi-ui/lib/es/sideSheet";
import STooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import { cn } from "../../lib/cn";
import { t } from "../../lib/i18n";

interface OverlayProps { open: boolean; onClose: () => void; title?: React.ReactNode; children?: React.ReactNode; className?: string }
export function Modal({ open, onClose, description, footer, bodyClassName, children, ...props }: OverlayProps & { description?: React.ReactNode; footer?: React.ReactNode; bodyClassName?: string }) {
  return <SModal {...props} visible={open} onCancel={onClose} footer={footer ?? null} width="min(94vw, 672px)" closeOnEsc motion={false} keepDOM={false} maskClosable={false}><div className={bodyClassName} onKeyDownCapture={event => {
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
