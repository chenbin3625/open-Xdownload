import { useIsFetching, useIsMutating } from "@tanstack/react-query";
import React, { useEffect, useState } from "react";

const SHOW_DELAY_MS = 300;

// 全局顶部加载进度条：任意接口请求（查询或变更）在途时在视口顶部显示。
// 延迟显示是为了避开任务页 5 秒轮询这类瞬时后台请求，只对真正慢的请求给出反馈。
export function GlobalLoadingBar() {
  const isFetching = useIsFetching();
  const isMutating = useIsMutating();
  const hasInflightRequests = isFetching > 0 || isMutating > 0;
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    if (!hasInflightRequests) {
      setVisible(false);
      return;
    }
    const timer = window.setTimeout(() => setVisible(true), SHOW_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [hasInflightRequests]);

  return (
    <div
      aria-hidden
      className={`fixed top-0 left-0 right-0 z-[1200] h-[3px] pointer-events-none overflow-hidden transition-opacity duration-200 ${
        visible ? "opacity-100" : "opacity-0"
      }`}
    >
      <div
        className={`h-full w-2/5 rounded-full bg-gradient-to-r from-sky-400 via-sky-500 to-indigo-500 ${
          visible ? "global-loading-bar-slide" : ""
        }`}
      />
    </div>
  );
}
