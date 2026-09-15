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
export type ButtonSize = "sm" | "md" | "lg";

const defaultStyle =
  "bg-surface text-fg-body border border-line shadow-xs hover:border-brand-400/40 hover:text-fg hover:bg-surface-hover active:bg-surface-muted";
const textStyle =
  "bg-transparent text-fg-body border border-transparent hover:bg-surface-hover hover:text-fg active:bg-surface-muted";

// 变体表集中管理配色，调用方只传语义名。
// 焦点环由全局 :focus-visible 提供，这里不重复定义。
const variantClass: Record<ButtonVariant, string> = {
  primary:
    "bg-gradient-to-b from-brand-500 to-brand-600 text-white border border-brand-400/25 shadow-xs shadow-brand-500/20 hover:from-brand-400 hover:to-brand-500 hover:shadow-brand-500/35 active:from-brand-600 active:to-brand-700",
  default: defaultStyle,
  secondary: defaultStyle,
  text: textStyle,
  ghost: textStyle,
  link: "bg-transparent text-brand-500 border border-transparent hover:text-brand-600 hover:underline active:opacity-80",
  danger:
    "bg-danger-soft text-danger border border-danger/30 hover:bg-danger hover:text-white hover:border-danger active:bg-danger/90",
};

const sizeClass: Record<ButtonSize, string> = {
  sm: "h-7 px-2.5 text-xs gap-1.5",
  md: "h-9 px-3.5 text-sm gap-2",
  lg: "h-10 px-4 text-sm gap-2",
};

const iconOnlySizeClass: Record<ButtonSize, string> = {
  sm: "size-7 p-0",
  md: "size-9 p-0",
  lg: "size-10 p-0",
};

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  icon?: React.ReactNode;
  block?: boolean;
  circle?: boolean;
  square?: boolean;
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
    square = false,
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
  const spinnerClass = size === "sm" ? "size-3.5" : "size-4";
  const showIcon = loading ? <Spinner className={spinnerClass} /> : icon;
  // 无文本内容且带有图标（或显式指定 circle/square）时，自动渲染为规整正方形/圆形，避免左右 padding 拉伸
  const isIconOnly = (!children && (Boolean(icon) || loading)) || circle || square;

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
        "inline-flex shrink-0 items-center justify-center font-medium leading-none",
        "transition-all duration-150 select-none cursor-pointer active:scale-[0.98]",
        "disabled:cursor-not-allowed disabled:opacity-50 disabled:active:scale-100",
        circle ? "rounded-full" : "rounded-control",
        variantClass[variant],
        isIconOnly ? iconOnlySizeClass[size] : sizeClass[size],
        block && "w-full",
        className,
      )}
      {...rest}
    >
      {/* asChild 时 Slot 只接受单个元素子节点，图标与 children 并列会报错。
          用 Slottable 圈出真正要被合并的那个子节点，图标留在外层。 */}
      {showIcon}
      {asChild ? <Slot.Slottable>{children}</Slot.Slottable> : children}
    </Component>
  );
});
