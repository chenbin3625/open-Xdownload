import { Eye, EyeOff, Search, X } from "lucide-react";
import React, { useState } from "react";
import { cn } from "../../lib/cn";

// antd 的 Input 把 prefix/suffix/清除按钮画在边框内部，焦点环落在外层包裹上。
// 这里用同一套结构：外层 affix 容器负责边框与焦点态，内部 input 去掉自身边框。
const affixBase =
  "flex items-center gap-2 rounded-control border border-line bg-surface " +
  "transition-colors focus-within:border-brand-500 " +
  "focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-brand-500 " +
  "has-disabled:cursor-not-allowed has-disabled:opacity-60";

const sizeClass = {
  sm: "h-7 px-2 text-xs",
  md: "h-9 px-3 text-sm",
} as const;

const bareInput =
  "min-w-0 flex-1 bg-transparent text-fg outline-none " +
  "placeholder:text-fg-subtle disabled:cursor-not-allowed";

// 原生 prefix 是 RDFa 的 string 属性，与这里的 ReactNode 装饰槽冲突，需先剔除。
export interface InputProps
  extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "size" | "prefix"> {
  size?: "sm" | "md";
  prefix?: React.ReactNode;
  suffix?: React.ReactNode;
  allowClear?: boolean;
  onPressEnter?: (event: React.KeyboardEvent<HTMLInputElement>) => void;
  onClear?: () => void;
  containerClassName?: string;
}

// 清空按钮要能在只传 onChange 的调用点上工作（与 antd 一致：清空等于
// 收到一次 value="" 的 onChange）。受控 input 直接改 .value 不会触发 React
// 的 onChange，需要走原生 setter + 派发 input 事件让 React 的合成事件接住。
function clearControlledInput(element: HTMLInputElement | null) {
  if (!element) return;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  setter?.call(element, "");
  element.dispatchEvent(new Event("input", { bubbles: true }));
}

export const Input = React.forwardRef<HTMLInputElement, InputProps>(function Input(
  {
    size = "md",
    prefix,
    suffix,
    allowClear = false,
    onPressEnter,
    onClear,
    containerClassName,
    className,
    onKeyDown,
    value,
    ...rest
  },
  ref,
) {
  const innerRef = React.useRef<HTMLInputElement>(null);
  const showClear = allowClear && !rest.disabled && typeof value === "string" && value.length > 0;

  return (
    <span className={cn(affixBase, sizeClass[size], containerClassName)}>
      {prefix && <span className="flex shrink-0 items-center text-fg-subtle">{prefix}</span>}
      <input
        ref={(node) => {
          innerRef.current = node;
          if (typeof ref === "function") ref(node);
          else if (ref) ref.current = node;
        }}
        value={value}
        className={cn(bareInput, className)}
        onKeyDown={(event) => {
          onKeyDown?.(event);
          if (event.key === "Enter") onPressEnter?.(event);
        }}
        {...rest}
      />
      {showClear && (
        <button
          type="button"
          aria-label="清空"
          onClick={() => {
            if (onClear) onClear();
            else clearControlledInput(innerRef.current);
            innerRef.current?.focus();
          }}
          className="flex shrink-0 cursor-pointer items-center rounded-full p-0.5 text-fg-subtle transition-colors hover:bg-surface-hover hover:text-fg-body"
        >
          <X className="size-3.5" />
        </button>
      )}
      {suffix && <span className="flex shrink-0 items-center text-fg-subtle">{suffix}</span>}
    </span>
  );
});

// 搜索框：项目里的两处都是随输入实时过滤，搜索按钮本就是装饰性的，
// 因此只放一个图标前缀 + 清除按钮，不做提交语义。
export const SearchInput = React.forwardRef<HTMLInputElement, InputProps>(function SearchInput(
  props,
  ref,
) {
  return <Input ref={ref} type="search" prefix={<Search className="size-3.5" />} {...props} />;
});

export const PasswordInput = React.forwardRef<HTMLInputElement, InputProps>(function PasswordInput(
  { suffix, ...rest },
  ref,
) {
  const [revealed, setRevealed] = useState(false);
  return (
    <Input
      ref={ref}
      type={revealed ? "text" : "password"}
      suffix={
        <span className="flex items-center gap-1">
          {suffix}
          <button
            type="button"
            aria-label={revealed ? "隐藏内容" : "显示内容"}
            aria-pressed={revealed}
            onClick={() => setRevealed((current) => !current)}
            className="flex cursor-pointer items-center rounded p-0.5 text-fg-subtle transition-colors hover:text-fg-body"
          >
            {revealed ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
          </button>
        </span>
      }
      {...rest}
    />
  );
});

export interface TextareaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> {
  containerClassName?: string;
}

export const Textarea = React.forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { className, containerClassName, ...rest },
  ref,
) {
  return (
    <span className={cn(affixBase, "px-3 py-2 text-sm", containerClassName)}>
      <textarea ref={ref} className={cn(bareInput, "resize-y", className)} {...rest} />
    </span>
  );
});

export interface NumberInputProps
  extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "size" | "value" | "onChange"> {
  value: number | null;
  onChange: (value: number | null) => void;
  min?: number;
  max?: number;
  step?: number;
  size?: "sm" | "md";
  addonAfter?: React.ReactNode;
}

// 受控数字输入。空串回传 null（三个调用点都靠 ?? 兜默认值，依赖这一契约），
// 失焦时按 min/max 收敛，方向键步进。
export const NumberInput = React.forwardRef<HTMLInputElement, NumberInputProps>(
  function NumberInput(
    { value, onChange, min, max, step = 1, size = "md", addonAfter, className, ...rest },
    ref,
  ) {
    const clamp = (next: number) => {
      let result = next;
      if (typeof min === "number") result = Math.max(min, result);
      if (typeof max === "number") result = Math.min(max, result);
      return result;
    };

    // 方向键按 step 步进，与原生 number input 的行为一致；
    // 值为空时从 min（没有 min 就从 0）起步。
    const stepBy = (direction: 1 | -1) => {
      onChange(clamp((value ?? min ?? 0) + direction * step));
    };

    return (
      <span className={cn(affixBase, sizeClass[size], className)}>
        <input
          ref={ref}
          type="number"
          inputMode="numeric"
          value={value ?? ""}
          min={min}
          max={max}
          step={step}
          aria-valuemin={min}
          aria-valuemax={max}
          aria-valuenow={value ?? undefined}
          className={cn(bareInput, "[appearance:textfield]")}
          onChange={(event) => {
            const raw = event.target.value;
            if (raw === "") {
              onChange(null);
              return;
            }
            const parsed = Number(raw);
            onChange(Number.isNaN(parsed) ? null : parsed);
          }}
          onBlur={() => {
            if (value !== null) onChange(clamp(value));
          }}
          onKeyDown={(event) => {
            if (event.key === "ArrowUp") {
              event.preventDefault();
              stepBy(1);
            } else if (event.key === "ArrowDown") {
              event.preventDefault();
              stepBy(-1);
            }
          }}
          {...rest}
        />
        {addonAfter && (
          <span className="shrink-0 border-l border-line pl-2 text-xs text-fg-muted">
            {addonAfter}
          </span>
        )}
      </span>
    );
  },
);

// 表单行布局。antd 的 Form.Item 在本项目里只用到 label + tooltip 两个能力
// （无校验、无 name、无表单状态），所以这里只做布局与 label 关联。
// children 是一个受控表单控件时传 htmlFor，label 与控件显式关联；
// 若不传就渲染成普通 span —— 悬空的 htmlFor 比没有 label 更糟，
// 读屏会朗读一个指向不存在元素的标签。
export function Field({
  label,
  tooltip,
  htmlFor,
  children,
  className,
}: {
  label: React.ReactNode;
  tooltip?: React.ReactNode;
  htmlFor?: string;
  children: React.ReactNode;
  className?: string;
}) {
  const labelClass = "flex items-center gap-1 text-xs font-medium text-fg-body";
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      {htmlFor ? (
        <label htmlFor={htmlFor} className={labelClass}>
          {label}
          {tooltip}
        </label>
      ) : (
        <span className={labelClass}>
          {label}
          {tooltip}
        </span>
      )}
      {children}
    </div>
  );
}
