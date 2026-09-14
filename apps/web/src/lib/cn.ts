import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// 合并类名并让后写的 Tailwind 工具类覆盖先写的同类属性。
// 组件内部先给默认样式，调用方通过 className 覆盖时不必关心冲突：
// cn("px-3 py-2", "px-4") -> "py-2 px-4"
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
