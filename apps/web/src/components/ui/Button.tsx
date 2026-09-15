import { Slot } from "radix-ui";
import React from "react";
import { cn } from "../../lib/cn";
import { Spinner } from "./Spinner";

export type ButtonVariant =
  | "primary"
  | "default"
  | "secondary"
  | "text"
  | "ghost"
  | "link"
  | "danger";
export type ButtonSize = "sm" | "md";

// 变体表集中管理配色，调用方只传语义名。
// 焦点环由全局 :focus-visible 提供，这里不重复定义。
const variantClass: Record<ButtonVariant, string> = {
  primary:
    "bg-gradient-to-b from-brand-500 to-brand-600 text-white border border-brand-400/25 shadow-xs shadow-brand-500/20 hover:from-brand-400 hover:to-brand-500 hover:shadow-brand-500/35 active:from-brand-600 active:to-brand-700",
  default:
    "bg-surface text-fg-body border border-line shadow-xs hover:border-brand-400/40 hover:text-fg hover:bg-surface-hover",
  secondary:
    "bg-surface text-fg-body border border-line shadow-xs hover:border-brand-400/40 hover:text-fg hover:bg-surface-hover",
  text: "bg-transparent text-fg-body border border-transparent hover:bg-surface-hover hover:text-fg",
  ghost: "bg-transparent text-fg-body border border-transparent hover:bg-surface-hover hover:text-fg",
  link: "bg-transparent text-brand-500 border border-transparent hover:text-brand-600 hover:underline",
  danger:
    "bg-danger-soft text-danger border border-danger/30 hover:bg-danger hover:text-white hover:border-danger",
};

const sizeClass: Record<ButtonSize, string> = {
  sm: "h-7 px-2.5 text-xs gap-1.5",
  md: "h-9 px-3.5 text-sm gap-2",
};

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  icon?: React.ReactNode;
  block?: boolean;
  circle?: boolean;
  asChild?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = "default",
    size = "md",
    loading = false,
    icon,
    block = false,
    circle = false,
    asChild = false,
    disabled,
    className,
    children,
    ...rest
  },
  ref,
) {
  const Component = asChild ? Slot.Root : "button";
  // loading 期间禁用，避免重复提交；与 antd Button 的行为一致。
  const isDisabled = disabled || loading;
  const showIcon = loading ? <Spinner className="size-3.5" /> : icon;

  return (
    <Component
      ref={ref}
      // asChild 时子元素自带语义标签（可能是 <a>），不能硬写 type；
      // disabled 也不是通用属性，改用 aria-disabled 表达。
      {...(asChild
        ? { "aria-disabled": isDisabled || undefined }
        : { type: rest.type ?? "button", disabled: isDisabled })}
      aria-busy={loading || undefined}
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded-control font-medium",
        "transition-all duration-150 select-none cursor-pointer active:scale-[0.98]",
        "disabled:cursor-not-allowed disabled:opacity-50 disabled:active:scale-100",
        variantClass[variant],
        sizeClass[size],
        circle && (size === "sm" ? "size-7 rounded-full px-0" : "size-9 rounded-full px-0"),
        block && "w-full",
        className,
      )}
      {...rest}
    >
      {/* asChild 时 Slot 只接受单个元素子节点，图标与 children 并列会报错。
          用 Slottable 圈出真正要被合并的那个子节点，图标留在外层。 */}
      {asChild ? (
        <>
          {showIcon}
          <Slot.Slottable>{children}</Slot.Slottable>
        </>
      ) : (
        <>
          {showIcon}
          {children}
        </>
      )}
    </Component>
  );
});
