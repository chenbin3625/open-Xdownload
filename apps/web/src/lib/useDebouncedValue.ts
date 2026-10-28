import { useEffect, useState } from "react";

/**
 * 返回 value 的防抖副本：value 连续变化时，只在静默 delay 毫秒后更新一次。
 * 用于搜索框——输入框本身保持即时响应，网络请求与查询键用防抖后的值。
 */
export function useDebouncedValue<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);

  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);

  return debounced;
}
