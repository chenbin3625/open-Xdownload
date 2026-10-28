import { Info } from "lucide-react";
import React from "react";
import { cn } from "../../lib/cn";

// 环境变量覆盖提示。GET /api/config 会带上 envOverridden: string[]（camelCase 字段名），
// 这些字段保存时后端会保留库内值、不会把 env 回显写进数据库 ——
// 在对应输入框下给出说明，避免用户以为"保存后 env 值就被改掉了"。
export function EnvOverrideNote({
  show,
  children = "由环境变量提供，保存不会覆盖",
  className,
}: {
  show?: boolean;
  children?: React.ReactNode;
  className?: string;
}) {
  if (!show) return null;
  return (
    <p className={cn("mt-1 flex items-center gap-1 text-[11px] text-fg-muted", className)}>
      <Info className="size-3 shrink-0 text-info" aria-hidden="true" />
      {children}
    </p>
  );
}

// 从配置对象里安全读取 envOverridden。字段可能缺失（老后端/未覆盖），
// 也可能混入非字符串（防御后端序列化异常），这里统一收敛成 string[]。
export function readEnvOverridden(config: unknown): string[] {
  if (!config || typeof config !== "object") return [];
  const value = (config as { envOverridden?: unknown }).envOverridden;
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string");
}
