import { X } from "lucide-react";
import { Dialog, Popover, Tooltip as RadixTooltip } from "radix-ui";
import React from "react";
import { cn } from "../../lib/cn";
import { Button } from "./Button";

// Radix Dialog 负责 portal / 焦点陷阱 / ESC / 滚动锁 / aria-modal，
// 这里只写 Tailwind 外观。
export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  className,
  bodyClassName,
}: {
  open: boolean;
  onClose: () => void;
  title?: React.ReactNode;
  description?: React.ReactNode;
  children?: React.ReactNode;
  footer?: React.ReactNode;
  className?: string;
  bodyClassName?: string;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={(next) => !next && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="animate-overlay-in fixed inset-0 z-[1200] bg-slate-950/50 backdrop-blur-sm" />
        <Dialog.Content
          className={cn(
            "animate-dialog-in fixed top-1/2 left-1/2 z-[1201] -translate-x-1/2 -translate-y-1/2",
            "flex max-h-[88vh] w-[min(92vw,42rem)] flex-col",
            "rounded-card border border-line bg-surface shadow-overlay",
            className,
          )}
        >
          {title && (
            <div className="flex items-start justify-between gap-3 border-b border-line px-5 py-4">
              <div className="min-w-0">
                <Dialog.Title className="text-base font-semibold text-fg">{title}</Dialog.Title>
                {description && (
                  <Dialog.Description className="mt-0.5 text-xs text-fg-muted">
                    {description}
                  </Dialog.Description>
                )}
              </div>
              <Dialog.Close asChild>
                <Button variant="text" size="sm" circle icon={<X className="size-4" />} aria-label="关闭" />
              </Dialog.Close>
            </div>
          )}
          <div className={cn("min-h-0 flex-1 overflow-y-auto px-5 py-4", bodyClassName)}>
            {children}
          </div>
          {footer && (
            <div className="flex items-center justify-end gap-2 border-t border-line px-5 py-3.5">
              {footer}
            </div>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

// 媒体预览用的无边框弹窗：内容自己撑满，不要标题栏与内边距。
export function MediaModal({
  open,
  onClose,
  title,
  children,
  className,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={(next) => !next && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="animate-overlay-in fixed inset-0 z-[1200] bg-slate-950/75 backdrop-blur-sm" />
        <Dialog.Content
          className={cn(
            "animate-dialog-in fixed top-1/2 left-1/2 z-[1201] -translate-x-1/2 -translate-y-1/2",
            "w-[min(94vw,68rem)] rounded-card border border-line bg-surface shadow-overlay",
            className,
          )}
        >
          <div className="flex items-center justify-between gap-3 border-b border-line px-4 py-2.5">
            <Dialog.Title className="min-w-0 truncate text-sm font-medium text-fg">
              {title}
            </Dialog.Title>
            <Dialog.Close asChild>
              <Button variant="text" size="sm" circle icon={<X className="size-4" />} aria-label="关闭" />
            </Dialog.Close>
          </div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

// 抽屉复用 Dialog 的行为，只把定位与入场动画换成侧滑。
export function Drawer({
  open,
  onClose,
  side = "right",
  title,
  width = "28rem",
  children,
  className,
}: {
  open: boolean;
  onClose: () => void;
  side?: "left" | "right";
  title?: React.ReactNode;
  width?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={(next) => !next && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="animate-overlay-in fixed inset-0 z-[1200] bg-slate-950/50 backdrop-blur-sm" />
        <Dialog.Content
          style={{ width: `min(100vw, ${width})` }}
          className={cn(
            "fixed inset-y-0 z-[1201] flex flex-col bg-surface shadow-overlay",
            side === "left"
              ? "animate-drawer-in-left left-0 border-r border-line"
              : "animate-drawer-in-right right-0 border-l border-line",
            className,
          )}
        >
          {/* Radix 要求 Content 内必须有 Title 以供读屏定位；
              无可见标题时用 VisuallyHidden 提供一个。 */}
          {title ? (
            <div className="flex items-center justify-between gap-3 border-b border-line px-4 py-3">
              <Dialog.Title className="min-w-0 text-sm font-semibold text-fg">{title}</Dialog.Title>
              <Dialog.Close asChild>
                <Button variant="text" size="sm" circle icon={<X className="size-4" />} aria-label="关闭" />
              </Dialog.Close>
            </div>
          ) : (
            <Dialog.Title className="sr-only">导航菜单</Dialog.Title>
          )}
          <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

export function TooltipProvider({ children }: { children: React.ReactNode }) {
  return (
    <RadixTooltip.Provider delayDuration={300} skipDelayDuration={150}>
      {children}
    </RadixTooltip.Provider>
  );
}

export function Tooltip({
  title,
  content,
  children,
  side = "top",
}: {
  title?: React.ReactNode;
  content?: React.ReactNode;
  children: React.ReactNode;
  side?: "top" | "right" | "bottom" | "left";
}) {
  const displayTitle = title ?? content;
  if (!displayTitle) return <>{children}</>;
  return (
    <RadixTooltip.Root>
      {/* asChild 把触发行为附加到子元素上，不额外包一层 DOM */}
      <RadixTooltip.Trigger asChild>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content
          side={side}
          sideOffset={6}
          className={cn(
            "z-[1300] max-w-xs rounded-control bg-slate-900 px-2.5 py-1.5 text-xs text-slate-100 shadow-md",
            "dark:bg-slate-100 dark:text-slate-900",
          )}
        >
          {displayTitle}
          <RadixTooltip.Arrow className="fill-slate-900 dark:fill-slate-100" />
        </RadixTooltip.Content>
      </RadixTooltip.Portal>
    </RadixTooltip.Root>
  );
}

// 二次确认气泡。Radix Popover 处理定位/点击外部关闭/焦点管理。
export function Popconfirm({
  title,
  description,
  okText = "确定",
  cancelText = "取消",
  danger = false,
  disabled = false,
  onConfirm,
  children,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  okText?: string;
  cancelText?: string;
  danger?: boolean;
  disabled?: boolean;
  onConfirm: () => void;
  children: React.ReactNode;
}) {
  const [open, setOpen] = React.useState(false);
  // disabled 时直接透传子元素，不挂弹出行为
  if (disabled) return <>{children}</>;

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>{children}</Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          sideOffset={6}
          collisionPadding={8}
          className={cn(
            "z-[1250] w-64 rounded-card border border-line bg-surface p-3 shadow-overlay",
            "animate-overlay-in",
          )}
        >
          <div className="text-sm font-medium text-fg">{title}</div>
          {description && <div className="mt-1 text-xs text-fg-muted">{description}</div>}
          <div className="mt-3 flex items-center justify-end gap-2">
            <Button size="sm" variant="text" onClick={() => setOpen(false)}>
              {cancelText}
            </Button>
            <Button
              size="sm"
              variant={danger ? "danger" : "primary"}
              onClick={() => {
                setOpen(false);
                onConfirm();
              }}
            >
              {okText}
            </Button>
          </div>
          <Popover.Arrow className="fill-surface" />
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

export function SimplePopover({
  trigger,
  children,
  side = "bottom",
  align = "end",
  className,
}: {
  trigger: React.ReactNode;
  children: React.ReactNode;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
  className?: string;
}) {
  const [open, setOpen] = React.useState(false);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>{trigger}</Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          side={side}
          align={align}
          sideOffset={8}
          collisionPadding={12}
          className={cn(
            "z-[1250] rounded-card border border-line bg-surface/95 p-3 shadow-overlay backdrop-blur-xl",
            "animate-overlay-in",
            className,
          )}
        >
          {typeof children === "function" ? (children as (close: () => void) => React.ReactNode)(() => setOpen(false)) : children}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
