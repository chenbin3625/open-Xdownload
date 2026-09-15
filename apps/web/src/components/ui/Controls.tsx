import { Check, ChevronDown } from "lucide-react";
import {
  Checkbox as RadixCheckbox,
  Select as RadixSelect,
  Switch as RadixSwitch,
  Tabs as RadixTabs,
  ToggleGroup,
} from "radix-ui";
import React from "react";
import { cn } from "../../lib/cn";
import { Spinner } from "./Spinner";

export interface SelectOption {
  value: string;
  label: string;
}

export function Select({
  value,
  onChange,
  options,
  placeholder = "请选择",
  size = "md",
  className,
  ariaLabel,
}: {
  value: string;
  onChange: (value: string) => void;
  options: SelectOption[];
  placeholder?: string;
  size?: "sm" | "md";
  className?: string;
  ariaLabel?: string;
}) {
  return (
    <RadixSelect.Root value={value} onValueChange={onChange}>
      <RadixSelect.Trigger
        aria-label={ariaLabel}
        className={cn(
          "inline-flex items-center justify-between gap-2 rounded-control",
          "border border-line bg-surface text-fg transition-colors",
          "hover:border-brand-400 data-[placeholder]:text-fg-subtle",
          size === "sm" ? "h-7 px-2.5 text-xs" : "h-9 px-3 text-sm",
          className,
        )}
      >
        <RadixSelect.Value placeholder={placeholder} />
        <RadixSelect.Icon>
          <ChevronDown className="size-3.5 shrink-0 text-fg-subtle" />
        </RadixSelect.Icon>
      </RadixSelect.Trigger>
      <RadixSelect.Portal>
        <RadixSelect.Content
          position="popper"
          sideOffset={4}
          className={cn(
            "z-[1250] max-h-72 min-w-[var(--radix-select-trigger-width)] overflow-hidden",
            "rounded-card border border-line bg-surface shadow-overlay",
          )}
        >
          <RadixSelect.Viewport className="p-1">
            {options.map((option) => (
              <RadixSelect.Item
                key={option.value}
                value={option.value}
                className={cn(
                  "relative flex cursor-pointer items-center gap-2 rounded-control py-1.5 pr-2 pl-7",
                  "text-sm text-fg-body outline-none select-none",
                  "data-highlighted:bg-surface-hover data-highlighted:text-fg",
                  "data-[state=checked]:font-medium data-[state=checked]:text-brand-600",
                )}
              >
                <RadixSelect.ItemIndicator className="absolute left-1.5 flex items-center">
                  <Check className="size-3.5" />
                </RadixSelect.ItemIndicator>
                <RadixSelect.ItemText>{option.label}</RadixSelect.ItemText>
              </RadixSelect.Item>
            ))}
          </RadixSelect.Viewport>
        </RadixSelect.Content>
      </RadixSelect.Portal>
    </RadixSelect.Root>
  );
}

export function Switch({
  checked,
  onChange,
  loading = false,
  disabled = false,
  ariaLabel,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  loading?: boolean;
  disabled?: boolean;
  ariaLabel?: string;
}) {
  return (
    <RadixSwitch.Root
      checked={checked}
      onCheckedChange={onChange}
      disabled={disabled || loading}
      aria-label={ariaLabel}
      className={cn(
        "relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full",
        "border border-transparent transition-colors",
        "data-[state=checked]:bg-brand-500 data-[state=unchecked]:bg-surface-hover",
        "data-[state=unchecked]:border-line",
        "disabled:cursor-not-allowed disabled:opacity-60",
      )}
    >
      <RadixSwitch.Thumb
        className={cn(
          "flex size-4 items-center justify-center rounded-full bg-white shadow-sm",
          "transition-transform duration-200",
          "data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0.5",
        )}
      >
        {loading && <Spinner className="size-2.5 text-brand-500" />}
      </RadixSwitch.Thumb>
    </RadixSwitch.Root>
  );
}

export function Checkbox({
  checked,
  onChange,
  label,
  id,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label?: React.ReactNode;
  id?: string;
}) {
  const generatedId = React.useId();
  const controlId = id ?? generatedId;
  return (
    <span className="inline-flex items-center gap-2">
      <RadixCheckbox.Root
        id={controlId}
        checked={checked}
        onCheckedChange={(next) => onChange(next === true)}
        className={cn(
          "flex size-4 shrink-0 cursor-pointer items-center justify-center rounded",
          "border border-line bg-surface transition-colors",
          "data-[state=checked]:border-brand-500 data-[state=checked]:bg-brand-500",
        )}
      >
        <RadixCheckbox.Indicator>
          <Check className="size-3 text-white" strokeWidth={3} />
        </RadixCheckbox.Indicator>
      </RadixCheckbox.Root>
      {label && (
        <label htmlFor={controlId} className="cursor-pointer text-sm text-fg-body select-none">
          {label}
        </label>
      )}
    </span>
  );
}

export interface SegmentedOption {
  value: string;
  label: React.ReactNode;
}

// 分段控制器。用 Radix ToggleGroup 拿到 radiogroup 语义与方向键导航；
// type="single" 且 value 受控，不允许取消选中（点当前项保持不变）。
export function Segmented({
  value,
  onChange,
  options,
  size = "sm",
  className,
  ariaLabel,
}: {
  value: string;
  onChange: (value: string) => void;
  options: SegmentedOption[];
  size?: "sm" | "md";
  className?: string;
  ariaLabel?: string;
}) {
  return (
    <ToggleGroup.Root
      type="single"
      value={value}
      onValueChange={(next) => {
        if (next) onChange(next);
      }}
      aria-label={ariaLabel}
      className={cn(
        "inline-flex items-center rounded-control border border-line bg-surface-muted",
        size === "sm" ? "h-7 gap-0.5 p-0.5" : "h-9 gap-1 p-1",
        className,
      )}
    >
      {options.map((option) => (
        <ToggleGroup.Item
          key={option.value}
          value={option.value}
          className={cn(
            "inline-flex cursor-pointer items-center justify-center gap-1.5 rounded-[0.375rem] font-medium whitespace-nowrap text-fg-muted transition-colors select-none",
            "hover:text-fg-body",
            "data-[state=on]:bg-surface data-[state=on]:text-fg data-[state=on]:shadow-xs",
            size === "sm" ? "h-5.5 px-2.5 text-xs" : "h-7 px-3 text-sm",
          )}
        >
          {option.label}
        </ToggleGroup.Item>
      ))}
    </ToggleGroup.Root>
  );
}

export interface TabItem {
  key: string;
  label: React.ReactNode;
  children: React.ReactNode;
}

export function Tabs({
  value,
  onChange,
  items,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  items: TabItem[];
  className?: string;
}) {
  return (
    <RadixTabs.Root value={value} onValueChange={onChange} className={className}>
      <RadixTabs.List className="flex items-center gap-1 border-b border-line">
        {items.map((item) => (
          <RadixTabs.Trigger
            key={item.key}
            value={item.key}
            className={cn(
              "relative cursor-pointer px-3 py-2 text-sm font-medium transition-colors",
              "text-fg-muted hover:text-fg-body",
              "data-[state=active]:text-brand-600",
              // 下划线用 after 伪元素，避免布局跳动
              "after:absolute after:inset-x-2 after:-bottom-px after:h-0.5 after:rounded-full",
              "after:bg-transparent data-[state=active]:after:bg-brand-500",
            )}
          >
            {item.label}
          </RadixTabs.Trigger>
        ))}
      </RadixTabs.List>
      {items.map((item) => (
        <RadixTabs.Content key={item.key} value={item.key} className="pt-3 outline-none">
          {item.children}
        </RadixTabs.Content>
      ))}
    </RadixTabs.Root>
  );
}
