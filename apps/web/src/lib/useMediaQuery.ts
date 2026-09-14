import { useEffect, useState } from "react";

// 与 Tailwind 默认断点保持一致，避免 CSS 与 JS 各用一套阈值。
// 之前 AppHeader 用 Tailwind lg(1024px) 隐藏连接状态、App.tsx 用 antd
// Grid 的 lg(992px) 切换布局，992–1024px 区间里布局已进桌面态但标签仍隐藏。
export const breakpoints = {
  sm: 640,
  md: 768,
  lg: 1024,
  xl: 1280,
} as const;

export function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(() => {
    if (typeof window === "undefined") return false;
    return window.matchMedia(query).matches;
  });

  useEffect(() => {
    const list = window.matchMedia(query);
    setMatches(list.matches);
    const onChange = (event: MediaQueryListEvent) => setMatches(event.matches);
    list.addEventListener("change", onChange);
    return () => list.removeEventListener("change", onChange);
  }, [query]);

  return matches;
}

/** 视口是否达到指定断点（含）。 */
export function useBreakpointUp(name: keyof typeof breakpoints) {
  return useMediaQuery(`(min-width: ${breakpoints[name]}px)`);
}
