import { useCallback, useEffect, useState } from "react";

export type ThemePreference = "light" | "dark" | "system";
export type ResolvedTheme = "light" | "dark";
export type ThemeAccent = "x-blue" | "titanium" | "indigo" | "emerald" | "amber";

const STORAGE_KEY = "open-xdownload-theme";
const ACCENT_STORAGE_KEY = "open-xdownload-accent";

export interface ThemeAccentOption {
  id: ThemeAccent;
  name: string;
  color: string;
  dotColor: string;
  description: string;
}

export const THEME_ACCENTS: readonly ThemeAccentOption[] = [
  {
    id: "x-blue",
    name: "𝕏 电光蓝",
    color: "#1d9bf0",
    dotColor: "bg-[#1d9bf0]",
    description: "经典 Twitter / X 原生电光蓝",
  },
  {
    id: "titanium",
    name: "Titanium 钛金",
    color: "#71717a",
    dotColor: "bg-[#71717a] dark:bg-[#e4e4e7]",
    description: "Linear 风格纯黑钛极简",
  },
  {
    id: "indigo",
    name: "Cyber 极光紫",
    color: "#6366f1",
    dotColor: "bg-[#6366f1]",
    description: "Raycast 极客科技感",
  },
  {
    id: "emerald",
    name: "Emerald 极光绿",
    color: "#10b981",
    dotColor: "bg-[#10b981]",
    description: "清新醒目信号绿",
  },
  {
    id: "amber",
    name: "Amber 琥珀金",
    color: "#f59e0b",
    dotColor: "bg-[#f59e0b]",
    description: "暖调温润琥珀金",
  },
] as const;

function isThemePreference(value: unknown): value is ThemePreference {
  return value === "light" || value === "dark" || value === "system";
}

function isThemeAccent(value: unknown): value is ThemeAccent {
  return THEME_ACCENTS.some((a) => a.id === value);
}

export function readThemePreference(): ThemePreference {
  if (typeof window === "undefined") return "system";
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    return isThemePreference(stored) ? stored : "system";
  } catch {
    // 隐私模式下 localStorage 可能抛异常，回退到跟随系统
    return "system";
  }
}

export function readAccentPreference(): ThemeAccent {
  if (typeof window === "undefined") return "x-blue";
  try {
    const stored = window.localStorage.getItem(ACCENT_STORAGE_KEY);
    return isThemeAccent(stored) ? (stored as ThemeAccent) : "x-blue";
  } catch {
    return "x-blue";
  }
}

function systemPrefersDark() {
  if (typeof window === "undefined" || !window.matchMedia) return false;
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

export function resolveTheme(preference: ThemePreference): ResolvedTheme {
  if (preference === "system") return systemPrefersDark() ? "dark" : "light";
  return preference;
}

// 类名直接写在 <html> 上，与 index.css 里的 @custom-variant dark 对应。
// 必须同时写显式的 .light：index.css 的首屏防闪回退用 html:not(.light):not(.dark)
// 判断“用户尚未选择”，若只切 .dark，系统偏好暗色而用户选了亮色时会被回退规则覆盖。
export function applyTheme(resolved: ResolvedTheme) {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  root.classList.toggle("dark", resolved === "dark");
  root.classList.toggle("light", resolved === "light");
}

export function applyAccent(accent: ThemeAccent) {
  if (typeof document === "undefined") return;
  document.documentElement.setAttribute("data-accent", accent);
}

export function useTheme() {
  const [preference, setPreference] = useState<ThemePreference>(readThemePreference);
  const [resolved, setResolved] = useState<ResolvedTheme>(() => resolveTheme(readThemePreference()));
  const [accent, setAccentState] = useState<ThemeAccent>(readAccentPreference);

  useEffect(() => {
    const next = resolveTheme(preference);
    setResolved(next);
    applyTheme(next);
    try {
      window.localStorage.setItem(STORAGE_KEY, preference);
    } catch {
      // 写入失败不影响当前会话的显示
    }
  }, [preference]);

  useEffect(() => {
    applyAccent(accent);
    try {
      window.localStorage.setItem(ACCENT_STORAGE_KEY, accent);
    } catch {
      // 写入失败不影响当前会话的显示
    }
  }, [accent]);

  // 仅在跟随系统时监听系统切换；用户显式选择后系统变化不应覆盖其选择。
  useEffect(() => {
    if (preference !== "system" || !window.matchMedia) return;
    const query = window.matchMedia("(prefers-color-scheme: dark)");
    const handleChange = (event: MediaQueryListEvent) => {
      const next: ResolvedTheme = event.matches ? "dark" : "light";
      setResolved(next);
      applyTheme(next);
    };
    query.addEventListener("change", handleChange);
    return () => query.removeEventListener("change", handleChange);
  }, [preference]);

  const toggle = useCallback(() => {
    // 切换只在明暗之间来回，不会把用户重新丢回 system
    setPreference(resolveTheme(readThemePreference()) === "dark" ? "light" : "dark");
  }, []);

  const setAccent = useCallback((nextAccent: ThemeAccent) => {
    setAccentState(nextAccent);
  }, []);

  return { preference, resolved, setPreference, toggle, accent, setAccent };
}
