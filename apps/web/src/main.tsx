import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ConfigProvider } from "antd";
import zhCN from "antd/locale/zh_CN";
import "antd/dist/reset.css";
import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { hydrateAppQueries, takeAppBootstrap } from "./lib/bootstrap";
import { readRouteState } from "./lib/useRouteState";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      retry: 1,
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
    <ConfigProvider button={{ autoInsertSpace: false }} locale={zhCN}>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </ConfigProvider>
  </React.StrictMode>,
);
