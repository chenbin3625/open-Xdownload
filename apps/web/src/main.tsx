import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "./index.css";
import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { TooltipProvider } from "./components/ui/Overlay";
import { Toaster } from "./components/ui/Toast";
import { hydrateAppQueries, takeAppBootstrap } from "./lib/bootstrap";
import { readRouteState } from "./lib/useRouteState";
import { applyTheme, readThemePreference, resolveTheme } from "./lib/useTheme";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});
// 在首次渲染前落定主题类名，避免明暗切换时的闪白。
applyTheme(resolveTheme(readThemePreference()));

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
