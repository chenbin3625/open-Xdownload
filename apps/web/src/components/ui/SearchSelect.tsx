import { Check, ChevronDown, Search } from "lucide-react";
import { Popover } from "radix-ui";
import React, { useMemo, useRef, useState } from "react";
import { cn } from "../../lib/cn";
import type { SelectOption } from "./Controls";

// 可搜索下拉。Radix Select 不支持在弹层里放输入框（它把按键当作 typeahead），
// 因此这里用 Popover + 自己实现 combobox 语义：
// role=combobox 触发器 + role=listbox 列表 + aria-activedescendant 指示高亮项。
export function SearchSelect({
  value,
  onChange,
  options,
  placeholder = "请选择",
  searchPlaceholder = "搜索...",
  size = "md",
  className,
  ariaLabel,
}: {
  value: string;
  onChange: (value: string) => void;
  options: SelectOption[];
  placeholder?: string;
  searchPlaceholder?: string;
  size?: "sm" | "md";
  className?: string;
  ariaLabel?: string;
}) {
  const [open, setOpen] = useState(false);
  const [keyword, setKeyword] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const listId = React.useId();
  const listRef = useRef<HTMLDivElement>(null);

  const filtered = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    if (!kw) return options;
    return options.filter((option) => option.label.toLowerCase().includes(kw));
  }, [keyword, options]);

  const selectedLabel = options.find((option) => option.value === value)?.label;

  function commit(index: number) {
    const option = filtered[index];
    if (!option) return;
    onChange(option.value);
    setOpen(false);
    setKeyword("");
  }

  // 高亮项滚动进可视区，长列表下键盘导航才跟得上
  function moveActive(delta: number) {
    setActiveIndex((current) => {
      if (filtered.length === 0) return 0;
      const next = (current + delta + filtered.length) % filtered.length;
      const element = listRef.current?.querySelector(`[data-index="${next}"]`);
      // scrollIntoView 在部分环境（jsdom）没有实现，滚动只是体验优化，不该让键盘导航崩掉
      element?.scrollIntoView?.({ block: "nearest" });
      return next;
    });
  }

  return (
    <Popover.Root
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setKeyword("");
          setActiveIndex(Math.max(0, filtered.findIndex((option) => option.value === value)));
        }
      }}
    >
      <Popover.Trigger
        role="combobox"
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-controls={open ? listId : undefined}
        aria-label={ariaLabel}
        className={cn(
          "inline-flex items-center justify-between gap-2 rounded-control",
          "border border-line bg-surface text-fg transition-colors hover:border-brand-400",
          size === "sm" ? "h-7 px-2.5 text-xs" : "h-9 px-3 text-sm",
          className,
        )}
      >
        <span className={cn("min-w-0 truncate", !selectedLabel && "text-fg-subtle")}>
          {selectedLabel ?? placeholder}
        </span>
        <ChevronDown className="size-3.5 shrink-0 text-fg-subtle" />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          sideOffset={4}
          align="start"
          className={cn(
            "z-[1250] w-[max(var(--radix-popover-trigger-width),14rem)] overflow-hidden",
            "rounded-card border border-line bg-surface shadow-overlay",
          )}
          onOpenAutoFocus={(event) => {
            // 交给输入框自己聚焦，避免 Radix 把焦点放到容器上
            event.preventDefault();
            listRef.current?.parentElement?.querySelector("input")?.focus();
          }}
        >
          <div className="flex items-center gap-2 border-b border-line px-2.5 py-2">
            <Search className="size-3.5 shrink-0 text-fg-subtle" />
            <input
              value={keyword}
              onChange={(event) => {
                setKeyword(event.target.value);
                setActiveIndex(0);
              }}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown") {
                  event.preventDefault();
                  moveActive(1);
                } else if (event.key === "ArrowUp") {
                  event.preventDefault();
                  moveActive(-1);
                } else if (event.key === "Enter") {
                  event.preventDefault();
                  commit(activeIndex);
                }
              }}
              placeholder={searchPlaceholder}
              aria-controls={listId}
              aria-activedescendant={
                filtered[activeIndex] ? `${listId}-${activeIndex}` : undefined
              }
              className="min-w-0 flex-1 bg-transparent text-sm text-fg outline-none placeholder:text-fg-subtle"
            />
          </div>
          <div ref={listRef} id={listId} role="listbox" className="max-h-64 overflow-y-auto p-1">
            {filtered.length === 0 ? (
              <p className="px-2 py-3 text-center text-xs text-fg-muted">无匹配项</p>
            ) : (
              filtered.map((option, index) => (
                <div
                  key={option.value}
                  id={`${listId}-${index}`}
                  data-index={index}
                  role="option"
                  aria-selected={option.value === value}
                  onClick={() => commit(index)}
                  onMouseEnter={() => setActiveIndex(index)}
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded-control py-1.5 pr-2 pl-7",
                    "relative text-sm text-fg-body select-none",
                    index === activeIndex && "bg-surface-hover text-fg",
                    option.value === value && "font-medium text-brand-600",
                  )}
                >
                  {option.value === value && (
                    <Check className="absolute left-1.5 size-3.5" />
                  )}
                  <span className="min-w-0 truncate">{option.label}</span>
                </div>
              ))
            )}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
