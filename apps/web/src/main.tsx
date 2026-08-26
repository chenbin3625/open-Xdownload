import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ConfigProvider } from "antd";
import zhCN from "antd/locale/zh_CN";
import "antd/dist/reset.css";
import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { hydrateAppQueries, takeAppBootstrap } from "./lib/bootstrap";
import { readRouteState } from "./lib/useRouteState";
import "./styles.css";

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
    <ConfigProvider
      button={{ autoInsertSpace: false }}
      locale={zhCN}
      theme={{
        token: {
          borderRadius: 8,
          colorBgContainer: "#ffffff",
          colorBgLayout: "#f4f7fb",
          colorBorder: "#d9e2ef",
          colorBorderSecondary: "#e8edf5",
          colorError: "#d14343",
          colorInfo: "#2563eb",
          colorPrimary: "#2563eb",
          colorSuccess: "#168a4a",
          colorText: "#18212f",
          colorTextSecondary: "#64748b",
          colorWarning: "#b7791f",
          controlHeight: 34,
          controlHeightLG: 42,
          fontFamily:
            'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
        },
        components: {
          Layout: {
            bodyBg: "#f4f7fb",
            siderBg: "#ffffff",
          },
        },
      }}
    >
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </ConfigProvider>
  </React.StrictMode>,
);
