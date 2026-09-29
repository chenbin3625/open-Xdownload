// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import { renderWithProviders, screen, waitFor } from "../../test/render";
import { toast } from "../ui/Toast";
import { CreateJobModal, detectQuickInputTab, parseScreenName } from "./CreateJobModal";

vi.mock("../../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api")>();
  return {
    ...actual,
    createJob: vi.fn(),
    createJobsBatch: vi.fn(),
    createArchiveSchedule: vi.fn(),
  };
});

describe("parseScreenName", () => {
  it.each([
    ["https://x.com/OpenAI", "OpenAI"],
    ["https://x.com/OpenAI/media", "OpenAI"],
    ["https://x.com/OpenAI?s=20", "OpenAI"],
    ["https://x.com/OpenAI#top", "OpenAI"],
    ["x.com/OpenAI", "OpenAI"],
    ["http://www.twitter.com/sama/", "sama"],
    ["https://mobile.twitter.com/@elon_musk", "elon_musk"],
    ["@sama", "sama"],
    ["sama", "sama"],
    ["44196397", "44196397"],
  ])("%s -> %s", (input, expected) => {
    expect(parseScreenName(input)).toBe(expected);
  });

  it("returns null for a link without a usable handle", () => {
    expect(parseScreenName("https://x.com/")).toBeNull();
    expect(parseScreenName("https://example.com/OpenAI")).toBeNull();
    expect(parseScreenName("https://x.com/this_handle_is_too_long")).toBeNull();
  });
});

describe("detectQuickInputTab", () => {
  it.each([
    ["https://x.com/OpenAI/status/1234567890", "tweet_link"],
    ["twitter.com/OpenAI/status/1234567890?s=20", "tweet_link"],
    ["https://x.com/i/lists/1647289190", "list"],
    ["https://twitter.com/lists/1647289190", "list"],
    ["1492019283", "list"],
    ["https://x.com/OpenAI", "user"],
    ["https://x.com/OpenAI/media", "user"],
    ["x.com/OpenAI?s=20", "user"],
    ["@sama", "user"],
    ["sama", "user"],
  ])("%s -> %s", (input, expected) => {
    expect(detectQuickInputTab(input)).toBe(expected);
  });
});

function renderModal(props: Partial<React.ComponentProps<typeof CreateJobModal>> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithProviders(
    <QueryClientProvider client={queryClient}>
      <CreateJobModal open onClose={() => {}} {...props} />
    </QueryClientProvider>,
  );
}

describe("CreateJobModal", () => {
  beforeEach(() => {
    vi.mocked(api.createJob).mockReset();
    vi.mocked(api.createJobsBatch).mockReset();
    vi.mocked(api.createArchiveSchedule).mockReset();
  });

  it("drops schedule wording once the single-tweet tab is selected", async () => {
    const user = userEvent.setup();
    renderModal({ initialKind: "schedule" });

    expect(screen.getByRole("button", { name: /保存定时计划/ })).toBeTruthy();

    await user.click(screen.getByRole("tab", { name: /单条推文/ }));

    expect(screen.queryByRole("button", { name: /保存定时计划/ })).toBeNull();
    expect(screen.getByRole("button", { name: /立即下载/ })).toBeTruthy();
    expect(screen.getByText("解析后点击立即下载入库")).toBeTruthy();
  });

  it("submits the tweet as a normal job and reports it as such", async () => {
    const user = userEvent.setup();
    const success = vi.spyOn(toast, "success");
    vi.mocked(api.createJob).mockResolvedValue({
      id: 9, kind: "tweet_link", status: "pending", input: "u", title: "t", progress: 0,
      message: "", createdAt: "", updatedAt: "",
    });
    renderModal({ initialKind: "schedule" });

    await user.click(screen.getByRole("tab", { name: /单条推文/ }));
    await user.type(
      screen.getByPlaceholderText("https://x.com/username/status/1234567890"),
      "https://x.com/OpenAI/status/1",
    );
    await user.click(screen.getByRole("button", { name: /立即下载/ }));

    await waitFor(() => expect(success).toHaveBeenCalled());
    expect(api.createJob).toHaveBeenCalledTimes(1);
    expect(api.createArchiveSchedule).not.toHaveBeenCalled();
    expect(success).toHaveBeenCalledWith(expect.objectContaining({ message: "任务创建成功" }));
    expect(success).not.toHaveBeenCalledWith(expect.objectContaining({ message: "定时归档计划已保存" }));
    success.mockRestore();
  });

  it("routes a profile link from quick input to the user tab", () => {
    renderModal({ initialInput: "https://x.com/OpenAI/media" });

    expect(screen.getByRole("tab", { name: /用户归档/, selected: true })).toBeTruthy();
    expect(screen.getByText("已识别: 1 个")).toBeTruthy();
  });

  it("adds the scheme to a scheme-less tweet link from quick input", () => {
    renderModal({ initialInput: "x.com/OpenAI/status/123" });

    expect(screen.getByRole("tab", { name: /单条推文/, selected: true })).toBeTruthy();
    expect(
      (screen.getByPlaceholderText("https://x.com/username/status/1234567890") as HTMLInputElement).value,
    ).toBe("https://x.com/OpenAI/status/123");
  });
});
