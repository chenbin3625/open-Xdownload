// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { parseBatchInput } from "../components/modals/CreateJobModal";
import { FilenameStrategy } from "../components/settings/FilenameStrategy";
import { TaskCenterPage } from "./TaskCenterPage";
import type { Job } from "../lib/api";

vi.mock("../lib/useMediaQuery", () => ({
  useBreakpointUp: () => true,
}));

const sampleJobs: Job[] = [
  {
    id: 101,
    kind: "user",
    title: "用户 @alice",
    input: "alice",
    status: "completed",
    progress: 100,
    message: "已完成",
    error: "",
    createdAt: "2026-11-20T10:00:00Z",
    updatedAt: "2026-11-20T10:05:00Z",
  },
  {
    id: 102,
    kind: "failed_retry",
    title: "自动重试失败推文",
    input: "failed_tweets",
    status: "downloading",
    progress: 40,
    message: "重试中",
    error: "",
    createdAt: "2026-11-20T09:00:00Z",
    updatedAt: "2026-11-20T10:02:00Z",
  },
];

describe("Issue #25 features", () => {
  beforeEach(() => {
    window.localStorage?.removeItem("open_xdownload_pin_failed_retry");
  });

  it("parseBatchInput recognizes targets separated by commas, Chinese commas, semicolons, and spaces", () => {
    const users = parseBatchInput(
      "https://x.com/alice, @bob，charlie; https://x.com/dave  @alice",
      "user",
    );
    expect(users.items.map((item) => item.input)).toEqual([
      "alice",
      "bob",
      "charlie",
      "dave",
    ]);
    expect(users.duplicates).toBe(1);
    expect(users.invalid).toBe(0);

    const tweets = parseBatchInput(
      "https://x.com/a/status/111111, https://x.com/b/status/222222 https://x.com/c/status/333333",
      "tweet_link",
    );
    expect(tweets.items.map((item) => item.input)).toEqual([
      "https://x.com/a/status/111111",
      "https://x.com/b/status/222222",
      "https://x.com/c/status/333333",
    ]);
  });

  it("FilenameStrategy supports [{date}]{text}-{index}.{ext} preset and custom {date} field", () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <FilenameStrategy
        value="[{date}]{text}-{index}.{ext}"
        onChange={onChange}
        disabled={false}
        labelPrefix=""
      />,
    );
    expect(screen.getByText("[日期]推文正文-序号")).toBeTruthy();

    rerender(
      <FilenameStrategy
        value="{date}_{text}_{index}.{ext}"
        onChange={onChange}
        disabled={false}
        labelPrefix=""
      />,
    );
    expect(screen.getByText("推文日期")).toBeTruthy();
  });

  it("TaskCenterPage pins failed_retry jobs and failed-tweet banner when pin toggle is enabled", () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <TaskCenterPage
          jobs={sampleJobs}
          failedTweetCount={3}
          pagination={{ page: 1, pageSize: 20, total: 2, totalPages: 1 }}
          onPageChange={vi.fn()}
          onPageSizeChange={vi.fn()}
          onOpenCreateModal={vi.fn()}
          onOpenFailedDrawer={vi.fn()}
        />
      </QueryClientProvider>,
    );

    const beforeLinks = screen
      .getAllByRole("button")
      .map((btn) => btn.textContent ?? "")
      .filter((text) => text.includes("用户 @alice") || text.includes("自动重试失败推文"));
    expect(beforeLinks[0]).toContain("用户 @alice");

    const pinButton = screen.getByRole("button", { name: "置顶失败重试" });
    fireEvent.click(pinButton);

    const afterLinks = screen
      .getAllByRole("button")
      .map((btn) => btn.textContent ?? "")
      .filter((text) => text.includes("用户 @alice") || text.includes("自动重试失败推文"));
    expect(afterLinks[0]).toContain("自动重试失败推文");
    expect(screen.getByText("已置顶")).toBeTruthy();
    expect(screen.getByText("当前有 3 条失败推文待重试")).toBeTruthy();
  });
});
