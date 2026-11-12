// @vitest-environment jsdom
import React from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { renderWithProviders, screen } from "../test/render";
import { getTweetProgress, getTweets, retryTweetMedia, type ArchivedTweet } from "../lib/archive";
import { api, type Job } from "../lib/api";
import { TweetsPage } from "./TweetsPage";

vi.mock("../lib/archive", async original => ({ ...await original<typeof import("../lib/archive")>(), getTweets: vi.fn(), getTweetProgress: vi.fn(), retryTweetMedia: vi.fn() }));
vi.mock("../lib/api", async original => ({ ...await original<typeof import("../lib/api")>(), api: vi.fn() }));

const tweet: ArchivedTweet = {
  id: "123", url: "https://x.com/alice/status/123", text: "Long archived body ".repeat(90),
  author: { id: "42", name: "Alice", screenName: "alice" }, createdAt: "2026-11-10T12:00:00Z",
  media: [{ id: "photo", type: "photo", url: "https://pbs.twimg.com/photo.jpg", previewUrl: "", bestUrl: "", variants: [] }],
  downloads: [{ id: 7, jobId: 9, tweetId: "123", mediaUrl: "https://pbs.twimg.com/photo.jpg", filePath: "asset:photo/photo.jpg", fileUrl: "/api/assets/original", assetUrls: { thumbnail: "/api/assets/thumbnail" }, bytes: 20, createdAt: "2026-11-10T12:00:00Z" }],
  jobIds: [9], bodyStatus: "completed", mediaStatus: "partial", mediaError: "One video failed",
  archivedAt: "2026-11-11T00:00:00Z", quotedTweetId: "122",
  related: [{ id: "122", url: "https://x.com/bob/status/122", author: { id: "43", name: "Bob", screenName: "bob" }, text: "Quoted text available offline", createdAt: "2026-11-09T00:00:00Z", media: [] }],
};
const job: Job = { id: 9, kind: "tweet_link", status: "completed_with_errors", input: tweet.url, title: "Alice archive", archiveMode: "both", progress: 1, message: "One media failed", createdAt: "2026-11-11T00:00:00Z", updatedAt: "2026-11-11T00:00:00Z" };

function renderArchive() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithProviders(<QueryClientProvider client={client}><TweetsPage /></QueryClientProvider>);
}

beforeEach(() => {
  vi.clearAllMocks(); window.history.replaceState(null, "", "/tweets");
  vi.mocked(getTweets).mockImplementation(async query => ({ items: [tweet], pagination: { page: Number(query.get("page") ?? 1), pageSize: 20, total: 40, totalPages: 2 } }));
  vi.mocked(getTweetProgress).mockResolvedValue({ items: [{ userId: "42", lastId: "123", headId: "123", cursor: "resume", status: "interrupted", error: "", count: 12, updatedAt: "2026-11-11T00:00:00Z" }] });
  vi.mocked(retryTweetMedia).mockResolvedValue(job);
  vi.mocked(api).mockResolvedValue(job);
});

it("keeps the full body and available relations when media fails, with local previews and independent retry", async () => {
  const user = userEvent.setup(); renderArchive();
  const body = await screen.findByText(tweet.text, { collapseWhitespace: false, trim: false });
  expect(body.textContent).toBe(tweet.text);
  expect(screen.getByText("Quoted text available offline")).toBeTruthy();
  expect(screen.getByText("One video failed")).toBeTruthy();
  expect(screen.queryByText("媒体部分归档")).toBeNull();
  expect(screen.queryByText("正文已归档")).toBeNull();
  expect(screen.queryByRole("button", { name: "#9" })).toBeNull();
  const image = screen.getByRole("img", { name: "alice · 123" });
  expect(image.getAttribute("src")).toBe("/api/assets/original");
  await user.click(screen.getByRole("button", { name: /查看图片.*alice.*123.*7/ }));
  expect(within(screen.getByRole("dialog")).getByRole("img", { name: "@alice · 123" }).getAttribute("src")).toBe("/api/assets/original");
  fireEvent.keyDown(document, { key: "Escape", keyCode: 27 });
  await user.click(screen.getByRole("button", { name: "重试媒体" }));
  await waitFor(() => expect(retryTweetMedia).toHaveBeenCalledWith("123", expect.anything()));
  expect(body.textContent).toBe(tweet.text);
});

it("defaults to all authors with newest first and supports reversing the timeline", async () => {
  const user = userEvent.setup(); renderArchive();
  await screen.findByText("Quoted text available offline");
  expect(vi.mocked(getTweets).mock.calls[0][0].get("author")).toBeNull();
  expect(vi.mocked(getTweets).mock.calls[0][0].get("sort")).toBe("desc");
  await user.click(screen.getByRole("combobox", { name: "排序顺序" }));
  await user.click(screen.getByRole("option", { name: "最早发布优先" }));
  await waitFor(() => expect(vi.mocked(getTweets).mock.calls.at(-1)?.[0].get("sort")).toBe("asc"));
  expect(screen.getByRole("button", { name: "发布时间" })).toBeTruthy();
  expect(screen.queryByText("同步进度")).toBeNull();
});

it("persists search, author and pagination in the URL and restores browser navigation", async () => {
  window.history.replaceState(null, "", "/tweets?author=alice&from=2026-11-01&mediaType=text");
  const user = userEvent.setup(); renderArchive();
  await screen.findByText("Quoted text available offline");
  const initial = vi.mocked(getTweets).mock.calls[0][0];
  expect(initial.get("author")).toBe("alice"); expect(initial.get("from")).toBe("2026-11-01"); expect(initial.get("mediaType")).toBe("text");
  await user.type(screen.getByRole("textbox", { name: "搜索正文" }), "offline");
  await waitFor(() => expect(new URLSearchParams(window.location.search).get("search")).toBe("offline"));
  await user.click(await screen.findByRole("button", { name: "第 2 页" }));
  await waitFor(() => expect(new URLSearchParams(window.location.search).get("page")).toBe("2"));
  act(() => { window.history.replaceState(null, "", "/tweets?author=bob&search=restored"); window.dispatchEvent(new PopStateEvent("popstate")); });
  await waitFor(() => expect((screen.getByRole("textbox", { name: "搜索正文" }) as HTMLInputElement).value).toBe("restored"));
  expect((screen.getByRole("textbox", { name: "作者" }) as HTMLInputElement).value).toBe("bob");
  await waitFor(() => expect(vi.mocked(getTweets).mock.calls.at(-1)?.[0].get("author")).toBe("bob"));
});

it("filters a chosen date range using API-compatible calendar dates and clears it", async () => {
  window.history.replaceState(null, "", "/tweets?from=2026-11-01&to=2026-11-30&page=2");
  const user = userEvent.setup(); renderArchive();
  await screen.findByText("Quoted text available offline");
  await user.click(screen.getByRole("button", { name: "发布时间" }));
  await user.click(await screen.findByRole("gridcell", { name: "2026-11-02" }));
  await user.click(screen.getByRole("gridcell", { name: "2026-11-10" }));
  await waitFor(() => expect(new URLSearchParams(window.location.search).get("from")).toBe("2026-11-02"));
  expect(new URLSearchParams(window.location.search).get("to")).toBe("2026-11-10");
  expect(new URLSearchParams(window.location.search).get("page")).toBeNull();
  await user.click(screen.getByRole("button", { name: "清除筛选" }));
  await waitFor(() => expect(vi.mocked(getTweets).mock.calls.at(-1)?.[0].get("from")).toBeNull());
});

it("filters reposts before pagination and restores and clears the filter through the URL", async () => {
  window.history.replaceState(null, "", "/tweets?page=2");
  const user = userEvent.setup(); renderArchive();
  await screen.findByText("Quoted text available offline");
  await user.click(screen.getByRole("radio", { name: "隐藏转推" }));
  await waitFor(() => expect(vi.mocked(getTweets).mock.calls.at(-1)?.[0].get("retweets")).toBe("exclude"));
  expect(new URLSearchParams(window.location.search).get("page")).toBeNull();
  await user.click(screen.getByRole("radio", { name: "仅转推" }));
  await waitFor(() => expect(vi.mocked(getTweets).mock.calls.at(-1)?.[0].get("retweets")).toBe("only"));
  act(() => { window.history.replaceState(null, "", "/tweets?retweets=exclude"); window.dispatchEvent(new PopStateEvent("popstate")); });
  await waitFor(() => expect((screen.getByRole("radio", { name: "隐藏转推" }) as HTMLInputElement).checked).toBe(true));
  await user.click(screen.getByRole("button", { name: "清除筛选" }));
  await waitFor(() => expect(vi.mocked(getTweets).mock.calls.at(-1)?.[0].get("retweets")).toBeNull());
});

it("shows reposted text once while preserving the source and other related tweets", async () => {
  const original = { ...tweet.related![0], text: "Original reposted text" };
  vi.mocked(getTweets).mockResolvedValue({ items: [{ ...tweet, text: "RT @bob: Original reposted text", retweetedTweetId: original.id, related: [original, { ...original, id: "121", text: "Additional quoted context" }] }], pagination: { page: 1, pageSize: 20, total: 1, totalPages: 1 } });
  renderArchive();
  expect(await screen.findByText("Original reposted text")).toBeTruthy();
  expect(screen.queryByText("RT @bob: Original reposted text")).toBeNull();
  expect(screen.getByText("Additional quoted context")).toBeTruthy();
  expect(screen.getByRole("link", { name: /转推来源.*122/ }).getAttribute("href")).toBe("/tweets?id=122");
  expect(screen.getByText("转推了 @bob")).toBeTruthy();
});

it("expands clipped text and keeps the chosen layout and page in the URL", async () => {
  const scrollHeight = vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(200);
  const clientHeight = vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(80);
  try {
    window.history.replaceState(null, "", "/tweets?page=2");
    const user = userEvent.setup(); renderArchive();
    const expand = (await screen.findAllByRole("button", { name: "展开全文" }))[0];
    expect((screen.getByRole("radio", { name: "全部推文" }) as HTMLInputElement).checked).toBe(true);
    expect(expand.getAttribute("aria-expanded")).toBe("false");
    await user.click(expand);
    expect(screen.getByRole("button", { name: "收起全文" }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText(tweet.text, { collapseWhitespace: false, trim: false }).textContent).toBe(tweet.text);
    await user.click(screen.getByRole("radio", { name: "舒适" }));
    expect(new URLSearchParams(window.location.search).get("view")).toBe("comfortable");
    expect(new URLSearchParams(window.location.search).get("page")).toBe("2");
    expect((screen.getByRole("radio", { name: "全部推文" }) as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByRole("button", { name: "收起全文" })).toBeNull();
  } finally {
    scrollHeight.mockRestore(); clientHeight.mockRestore();
  }
});

it.each(["completed", "not_requested"])("does not offer media retry for %s", async mediaStatus => {
  vi.mocked(getTweets).mockResolvedValue({ items: [{ ...tweet, mediaStatus }], pagination: { page: 1, pageSize: 20, total: 1, totalPages: 1 } });
  renderArchive();
  await screen.findByText("Quoted text available offline");
  expect(screen.queryByRole("button", { name: "重试媒体" })).toBeNull();
});

it("keeps canceled pending media retryable and disables the command while queuing", async () => {
  vi.mocked(getTweets).mockResolvedValue({ items: [{ ...tweet, mediaStatus: "pending" }], pagination: { page: 1, pageSize: 20, total: 1, totalPages: 1 } });
  let finish!: (value: Job) => void;
  vi.mocked(retryTweetMedia).mockReturnValue(new Promise(resolve => { finish = resolve; }));
  const user = userEvent.setup(); renderArchive();
  const button = await screen.findByRole("button", { name: "重试媒体" });
  await user.click(button);
  await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(true));
  await user.click(button);
  expect(retryTweetMedia).toHaveBeenCalledTimes(1);
  await act(async () => { finish(job); });
});

it("does not label pure-text archives as missing media", async () => {
  vi.mocked(getTweets).mockResolvedValue({ items: [{ ...tweet, media: [], downloads: [], mediaStatus: "not_requested" }], pagination: { page: 1, pageSize: 20, total: 1, totalPages: 1 } });
  renderArchive();
  await screen.findByText("Quoted text available offline");
  expect(screen.queryByText("媒体尚未归档")).toBeNull();
  expect(screen.queryByRole("button", { name: "重试媒体" })).toBeNull();
});

it.each(["m4v", "ogv"])("plays archived .%s media as video with a remote poster fallback", async extension => {
  vi.mocked(getTweets).mockResolvedValue({ items: [{ ...tweet, downloads: [{ ...tweet.downloads[0], filePath: `asset:video/clip.${extension}`, fileUrl: "/api/assets/video", assetUrls: {} }] }], pagination: { page: 1, pageSize: 20, total: 1, totalPages: 1 } });
  const { container } = renderArchive();
  await screen.findByText(tweet.text, { collapseWhitespace: false, trim: false });
  const video = container.querySelector("video");
  expect(video?.getAttribute("src")).toBe("/api/assets/video");
  expect(video?.getAttribute("poster")).toBe("/api/library/downloads/7/preview");
  expect(screen.queryByRole("img", { name: "alice · 123" })).toBeNull();
});
