import React, { useEffect, useId, useRef, useState } from "react";
import SModal from "@douyinfe/semi-ui/lib/es/modal";
import SPopconfirm from "@douyinfe/semi-ui/lib/es/popconfirm";
import Popover from "@douyinfe/semi-ui/lib/es/popover";
import SideSheet from "@douyinfe/semi-ui/lib/es/sideSheet";
import STooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import { cn } from "../../lib/cn";
import { t } from "../../lib/i18n";

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "area[href]",
  "button:not([disabled])",
  'input:not([disabled]):not([type="hidden"])',
  "select:not([disabled])",
  "textarea:not([disabled])",
  "summary",
  "audio[controls]",
  "video[controls]",
  '[contenteditable]:not([contenteditable="false"])',
  '[tabindex]:not([tabindex="-1"])',
].join(", ");

const CLOSED_POPUP_TRIGGER_SELECTOR = '[aria-expanded="false"], [data-popupid]:not([aria-expanded="true"])';

function getFocusableElements(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(item => {
    if (item.closest('[hidden], [aria-hidden="true"], [inert]')) return false;
    if (item.matches(":disabled")) return false;
    if (item.style.display === "none" || item.style.visibility === "hidden") return false;
    const computed = window.getComputedStyle(item);
    if (computed.display === "none" || computed.visibility === "hidden") return false;
    return true;
  });
}

function stopEscPropagation(event?: KeyboardEvent | React.KeyboardEvent) {
  if (!event) return;
  event.preventDefault?.();
  event.stopPropagation?.();
  if ("nativeEvent" in event) {
    event.nativeEvent.stopPropagation();
    event.nativeEvent.stopImmediatePropagation?.();
  } else {
    event.stopImmediatePropagation?.();
  }
}

interface OverlayProps { open: boolean; onClose: () => void; title?: React.ReactNode; children?: React.ReactNode; className?: string }
export function Modal({ open, onClose, description, footer, bodyClassName, width = "min(94vw, 672px)", children, ...props }: OverlayProps & { description?: React.ReactNode; footer?: React.ReactNode; bodyClassName?: string; width?: string | number }) {
  return <SModal {...props} visible={open} onCancel={onClose} footer={footer ?? null} width={width} closeOnEsc motion={false} keepDOM={false} maskClosable={false}><div className={bodyClassName} onKeyDownCapture={event => {
    const target = event.target as HTMLElement;
    if (event.key === "Escape" && !event.nativeEvent.isComposing && target.closest(CLOSED_POPUP_TRIGGER_SELECTOR) && target.closest('[role="dialog"]') === event.currentTarget.closest('[role="dialog"]')) {
      stopEscPropagation(event);
      onClose();
    }
  }}>{description && <p className="mb-4 text-fg-muted">{description}</p>}{children}</div></SModal>;
}
export function MediaModal({ open, onClose, ...props }: OverlayProps) { return <SModal {...props} visible={open} onCancel={onClose} footer={null} width="min(96vw, 1088px)" closeOnEsc motion={false} keepDOM={false} />; }
export function Drawer({ open, onClose, side = "right", width = "28rem", title, ...props }: OverlayProps & { side?: "left" | "right"; width?: string }) {
  const id = useId(); const heading = useRef<HTMLSpanElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    if (!open) return;
    const dialog = heading.current?.closest<HTMLElement>('[role="dialog"]');
    if (!dialog) return;
    const ownPortal = dialog.closest(".semi-portal");
    dialog.setAttribute("aria-labelledby", id); dialog.setAttribute("aria-modal", "true"); dialog.tabIndex = -1;
    const previous = document.activeElement as HTMLElement | null;
    (getFocusableElements(dialog)[0] ?? dialog).focus();
    function trap(event: KeyboardEvent) {
      if (event.key !== "Tab" || !dialog) return;
      const active = document.activeElement as HTMLElement | null;
      const activePortal = active?.closest(".semi-portal");
      // 焦点位于 Drawer 之上的嵌套浮层（如 Popconfirm/Select 下拉）时，不抢夺焦点。
      if (activePortal && activePortal !== ownPortal) return;
      const items = getFocusableElements(dialog);
      if (!items.length) {
        event.preventDefault();
        dialog.focus();
        return;
      }
      if (!active || !dialog.contains(active) || active === dialog) {
        event.preventDefault();
        (event.shiftKey ? items[items.length - 1] : items[0]).focus();
        return;
      }
      const index = items.indexOf(active);
      if (event.shiftKey ? index <= 0 : index === -1 || index === items.length - 1) {
        event.preventDefault();
        (event.shiftKey ? items[items.length - 1] : items[0]).focus();
      }
    }
    function handleClosedTriggerEsc(event: KeyboardEvent) {
      if (event.key !== "Escape" || event.isComposing || !dialog) return;
      const target = event.target as HTMLElement | null;
      if (!target || target.closest('[role="dialog"]') !== dialog) return;
      if (target.closest(CLOSED_POPUP_TRIGGER_SELECTOR)) {
        stopEscPropagation(event);
        onCloseRef.current();
      }
    }
    document.addEventListener("keydown", trap);
    dialog.addEventListener("keydown", handleClosedTriggerEsc, true);
    return () => {
      document.removeEventListener("keydown", trap);
      dialog.removeEventListener("keydown", handleClosedTriggerEsc, true);
      if (previous?.isConnected) previous.focus();
    };
  }, [open, id]);
  return <SideSheet {...props} title={<span ref={heading} id={id} className={title ? undefined : "sr-only"}>{title ?? t("详情")}</span>} visible={open} onCancel={onClose} placement={side} width={`min(100vw, ${width})`} closeOnEsc motion={false} disableScroll keepDOM={false} />;
}
export function Tooltip({ title, content, children, side = "top" }: { title?: React.ReactNode; content?: React.ReactNode; children: React.ReactNode; side?: "top" | "right" | "bottom" | "left" }) { return <STooltip content={title ?? content} position={side} zIndex={1400}>{children}</STooltip>; }
export function Popconfirm({ title, description, okText, cancelText, danger, disabled, children, onConfirm }: { title: React.ReactNode; description?: React.ReactNode; okText?: string; cancelText?: string; danger?: boolean; disabled?: boolean; children: React.ReactNode; onConfirm: () => void }) {
  const id = useId(); const [visible, setVisible] = useState(false); const triggerRef = useRef<HTMLElement | null>(null);
  const effectiveVisible = !disabled && visible;
  useEffect(() => { if (disabled) setVisible(false); }, [disabled]);
  const captureTrigger = (target: EventTarget | null) => { if (target instanceof HTMLElement) triggerRef.current = target; };
  const restoreFocus = () => { if (triggerRef.current?.isConnected) triggerRef.current.focus(); };
  useEffect(() => {
    if (!effectiveVisible) return;
    const handleEsc = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.isComposing) return;
      stopEscPropagation(event);
      setVisible(false);
      restoreFocus();
    };
    window.addEventListener("keydown", handleEsc, true);
    return () => window.removeEventListener("keydown", handleEsc, true);
  }, [effectiveVisible]);
  const trigger = React.isValidElement<{ onClick?: React.MouseEventHandler }>(children)
    ? React.cloneElement(children, {
      onClick: event => {
        captureTrigger(event.currentTarget);
        children.props.onClick?.(event);
        if (!disabled) setVisible(current => !current);
      },
    })
    : <span onClick={event => { captureTrigger(event.currentTarget); if (!disabled) setVisible(true); }}>{children}</span>;
  const popconfirmExtraProps = {
    wrapWhenSpecial: false,
    onEscKeyDown: (event?: KeyboardEvent | React.KeyboardEvent) => {
      if (!effectiveVisible) return;
      stopEscPropagation(event);
      setVisible(false);
      restoreFocus();
    },
  } as Partial<React.ComponentProps<typeof SPopconfirm>>;
  return <SPopconfirm {...popconfirmExtraProps} visible={effectiveVisible} onVisibleChange={next => { if (!disabled) setVisible(next); }} onClickOutSide={() => setVisible(false)} title={<span id={`${id}-title`} ref={node => { const dialog = node?.closest('[role="alertdialog"], [role="dialog"]'); dialog?.setAttribute("role", "alertdialog"); dialog?.setAttribute("aria-labelledby", `${id}-title`); if (description) dialog?.setAttribute("aria-describedby", `${id}-description`); }}>{title}</span>} content={description ? <span id={`${id}-description`}>{description}</span> : undefined} okText={okText ?? t("确定")} cancelText={cancelText ?? t("取消")} okType={danger ? "danger" : "primary"} onConfirm={() => { if (disabled) return; onConfirm(); setVisible(false); }} onCancel={() => setVisible(false)} motion={false} keepDOM={false} zIndex={1350}>{trigger}</SPopconfirm>;
}
function popoverPosition(side: "top" | "right" | "bottom" | "left", align: "start" | "center" | "end") {
  if (align === "center") return side;
  if (side === "top" || side === "bottom") return `${side}${align === "start" ? "Left" : "Right"}` as const;
  return `${side}${align === "start" ? "Top" : "Bottom"}` as const;
}
export function SimplePopover({ trigger, children, side = "bottom", align = "end", className }: { trigger: React.ReactNode; children: React.ReactNode; side?: "top" | "right" | "bottom" | "left"; align?: "start" | "center" | "end"; className?: string }) { return <Popover trigger="click" position={popoverPosition(side, align)} content={<div className={cn("p-3", className)}>{children}</div>} zIndex={1350}>{trigger}</Popover>; }
