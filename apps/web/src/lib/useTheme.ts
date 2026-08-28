import { useCallback, useEffect, useState } from "react";

export type AppTheme = "dark" | "light";

const THEME_STORAGE_KEY = "open_xdownload_theme";

export function getInitialTheme(): AppTheme {
  if (typeof window === "undefined") return "dark";
  const stored = localStorage.getItem(THEME_STORAGE_KEY);
  if (stored === "dark" || stored === "light") {
    return stored;
  }
  return window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

export function useTheme() {
  const [theme, setTheme] = useState<AppTheme>(() => getInitialTheme());

  useEffect(() => {
    const root = document.documentElement;
    const body = document.body;
    if (theme === "dark") {
      root.classList.add("dark");
      root.classList.remove("light");
      if (body) {
        body.classList.remove("bg-slate-50", "text-slate-900");
        body.classList.add("bg-slate-950", "text-slate-100");
      }
    } else {
      root.classList.remove("dark");
      root.classList.add("light");
      if (body) {
        body.classList.remove("bg-slate-950", "text-slate-100");
        body.classList.add("bg-slate-50", "text-slate-900");
      }
    }
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  }, [theme]);

  const toggleTheme = useCallback(() => {
    setTheme((prev) => (prev === "dark" ? "light" : "dark"));
  }, []);

  return {
    theme,
    isDark: theme === "dark",
    setTheme,
    toggleTheme,
  };
}
