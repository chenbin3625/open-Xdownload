import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "./index.css";
import "@douyinfe/semi-ui/lib/es/_base/base.css";
import "./lib/i18n";
import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { ApiError } from "./lib/api";
import { TooltipProvider } from "./components/semi";
import { Toaster } from "./components/semi";
import { hydrateAppQueries, takeAppBootstrap } from "./lib/bootstrap";
import { readRouteState } from "./lib/useRouteState";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      // 超时（AbortError）和 401 不重试：前者重试只会再等一个完整超时窗口，
      // 后者已触发 session invalidation，重试不会改变结果。
      retry: (failureCount, error) => {
        if (failureCount >= 1) return false;
        if (error instanceof DOMException && error.name === "AbortError") return false;
        if (error instanceof ApiError && error.status === 401) return false;
        return true;
      },
    },
  },
});
const bootstrap = takeAppBootstrap();
if (bootstrap) {
  const route = readRouteState();
  hydrateAppQueries(queryClient, bootstrap, route.jobPage, route.jobPageSize);
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      {/* Tooltip 的延迟与分组行为需要一个全局 Provider；
          Toaster 只挂一次，命令式 toast.* 由它统一渲染。 */}
      <TooltipProvider>
        <App />
        <Toaster />
      </TooltipProvider>
    </QueryClientProvider>
  </React.StrictMode>,
);
