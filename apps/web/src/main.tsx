import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
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
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </React.StrictMode>,
);
