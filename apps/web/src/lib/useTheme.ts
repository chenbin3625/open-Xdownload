import { useCallback, useEffect, useState } from "react";

export type ThemePreference = "light" | "dark" | "system";
export type ResolvedTheme = "light" | "dark";

const STORAGE_KEY = "open-xdownload-theme";

function isThemePreference(value: unknown): value is ThemePreference {
  return value === "light" || value === "dark" || value === "system";
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

export function useTheme() {
  const [preference, setPreference] = useState<ThemePreference>(readThemePreference);
  const [resolved, setResolved] = useState<ResolvedTheme>(() => resolveTheme(readThemePreference()));

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

  return { preference, resolved, setPreference, toggle };
}
