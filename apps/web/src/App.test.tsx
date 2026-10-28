// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App from "./App";
import { TooltipProvider } from "./components/ui/Overlay";
import type { Job } from "./lib/api";

// jsdom 没有 EventSource / matchMedia，这里给最小桩。
class FakeEventSource {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((message: { data: string }) => void) | null = null;
  readyState = 0;
  constructor(readonly url: string) {}
  close() {
    this.readyState = 2;
  }
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const jobs: Job[] = [
  {
    id: 1, kind: "tweet_link", status: "completed", input: "https://x.com/u/status/1",
    title: "成功任务", progress: 1, message: "下载完成",
    createdAt: "2026-09-15T08:00:00Z", updatedAt: "2026-09-15T08:01:00Z",
  },
  {
    id: 2, kind: "user", status: "failed", input: "@failed_user",
    title: "失败任务", progress: 0, message: "任务失败", error: "用户不存在",
    createdAt: "2026-09-15T09:00:00Z", updatedAt: "2026-09-15T09:01:00Z",
  },
];

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <App />
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

function goTo(path: string) {
  window.history.replaceState(null, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

beforeEach(() => {
  if (!window.matchMedia) {
    window.matchMedia = ((query: string) => ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      onchange: null,
      dispatchEvent: () => false,
    })) as unknown as typeof window.matchMedia;
  }
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname === "/api/jobs") return json({ items: jobs, page: 1, pageSize: 20 });
      if (url.pathname === "/api/dashboard/meta") {
        return json({
          stats: { total: jobs.length, active: 0, completed: 1, failed: 1 },
          failedTweetCount: 0,
        });
      }
      if (url.pathname === "/api/archive-schedules") return json([]);
      if (url.pathname === "/api/library/downloads") {
        return json({
          items: [],
          pagination: { page: 1, pageSize: 60, total: 0, totalPages: 1 },
          counts: { all: 0, images: 0, videos: 0, gifs: 0 },
          users: [],
          truncated: false,
        });
      }
      return json({});
    }),
  );
  window.history.replaceState(null, "", "/tasks");
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("App 的 URL 状态", () => {
  it("启动时按 URL 还原任务中心的状态筛选", async () => {
    window.history.replaceState(null, "", "/tasks?status=failed");
    renderApp();

    await waitFor(() => expect(screen.getByText("失败任务")).toBeTruthy());
    expect(screen.queryByText("成功任务")).toBeNull();
  });

  it("切换状态筛选会把 URL 上的 status 去掉", async () => {
    window.history.replaceState(null, "", "/tasks?status=failed");
    renderApp();
    await waitFor(() => expect(screen.getByText("失败任务")).toBeTruthy());

    const completedTabs = screen.getAllByText(/^已完成/);
    await userEvent.click(completedTabs[completedTabs.length - 1]);

    await waitFor(() => expect(window.location.search).not.toContain("status=failed"));
    await waitFor(() => expect(screen.getByText("成功任务")).toBeTruthy());
  });

  it("媒体库页面的分类筛选会写进 URL，并可刷新还原", async () => {
    renderApp();
    goTo("/gallery");

    await waitFor(() => expect(screen.getByText("媒体归档库")).toBeTruthy());
    await userEvent.click(screen.getByText(/^视频 \(/));

    await waitFor(() => expect(window.location.search).toContain("kind=videos"));
    expect(window.location.pathname).toBe("/gallery");
  });
});
