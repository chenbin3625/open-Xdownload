import React from "react";
import { cn } from "../../lib/cn";

export interface CardProps {
  children?: React.ReactNode;
  title?: React.ReactNode;
  extra?: React.ReactNode;
  cover?: React.ReactNode;
  actions?: React.ReactNode[];
  hoverable?: boolean;
  className?: string;
  bodyClassName?: string;
  size?: "sm" | "md";
}

// 对应 antd Card 的 title / extra / cover / actions 四个插槽。
// bodyClassName 取代原先散落各处的 styles={{ body: { padding } }}。
export function Card({
  children,
  title,
  extra,
  cover,
  actions,
  hoverable = false,
  className,
  bodyClassName,
  size = "md",
}: CardProps) {
  return (
    <div
      className={cn(
        "overflow-hidden rounded-card border border-line/80 bg-surface/90 backdrop-blur-sm shadow-card",
        "transition-all duration-200",
        hoverable && "hover:border-brand-500/40 hover:shadow-raised hover:-translate-y-0.5",
        className,
      )}
    >
      {cover}
      {(title || extra) && (
        <div
          className={cn(
            "flex items-center justify-between gap-3 border-b border-line",
            size === "sm" ? "px-4 py-2.5" : "px-5 py-3.5",
          )}
        >
          <div className="min-w-0 text-sm font-semibold text-fg">{title}</div>
          {extra && <div className="shrink-0">{extra}</div>}
        </div>
      )}
      {children !== undefined && (
        <div className={cn(size === "sm" ? "p-4" : "p-5", bodyClassName)}>{children}</div>
      )}
      {actions && actions.length > 0 && (
        <div className="flex items-stretch border-t border-line">
          {actions.map((action, index) => (
            <div
              key={index}
              className={cn(
                "flex flex-1 items-center justify-center py-2",
                index > 0 && "border-l border-line",
              )}
            >
              {action}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// 页面分节标题，替代设置页里用 Card title 承担的分节语义。
export function SectionCard({
  title,
  description,
  icon,
  children,
  className,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  icon?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "overflow-hidden rounded-card border border-line bg-surface shadow-card",
        className,
      )}
    >
      <div className="flex items-start gap-3 border-b border-line px-5 py-4">
        {icon && (
          <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-control bg-brand-50 text-brand-600 dark:bg-brand-500/12">
            {icon}
          </span>
        )}
        <div className="min-w-0">
          <h3 className="text-sm font-semibold text-fg">{title}</h3>
          {description && <p className="mt-0.5 text-xs text-fg-muted">{description}</p>}
        </div>
      </div>
      <div className="p-5">{children}</div>
    </div>
  );
}
