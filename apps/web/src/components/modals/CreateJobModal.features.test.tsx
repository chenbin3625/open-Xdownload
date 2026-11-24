// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import type { ArchivedTweet } from "../../lib/archive";
import { renderWithProviders, screen, waitFor } from "../../test/render";
import { TweetCard } from "../tweets/TweetCard";
import { CreateJobModal } from "./CreateJobModal";

vi.mock("../../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api")>();
  return {
    ...actual,
    createJob: vi.fn(),
    createJobsBatch: vi.fn(),
    createArchiveSchedule: vi.fn(),
    parseTweetLink: vi.fn(),
  };
});

function renderModal(props: Partial<React.ComponentProps<typeof CreateJobModal>> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithProviders(
    <QueryClientProvider client={queryClient}>
      <CreateJobModal open onClose={() => {}} {...props} />
    </QueryClientProvider>,
  );
}

describe("v15 frontend features", () => {
  beforeEach(() => {
    vi.mocked(api.createJob).mockReset();
    vi.mocked(api.createJobsBatch).mockReset();
    vi.mocked(api.createArchiveSchedule).mockReset();
  });

  it("submits batch tweet links via createJobsBatch in multi-line mode", async () => {
    const user = userEvent.setup();
    vi.mocked(api.createJobsBatch).mockResolvedValue([
      { id: 1, kind: "tweet_link", status: "pending", input: "", title: "", progress: 0, message: "", createdAt: "", updatedAt: "" },
      { id: 2, kind: "tweet_link", status: "pending", input: "", title: "", progress: 0, message: "", createdAt: "", updatedAt: "" },
    ]);
    renderModal({ initialKind: "tweet_link" });

    await user.click(screen.getByRole("button", { name: "批量多条链接" }));
    const textarea = screen.getByRole("textbox", { name: "批量推文链接" });
    await user.type(textarea, "https://x.com/alice/status/111{Enter}https://x.com/bob/status/222");
    await user.click(screen.getByRole("button", { name: /批量下载 \(2\)/ }));

    await waitFor(() => expect(api.createJobsBatch).toHaveBeenCalledWith({
      items: [
        { kind: "tweet_link", input: "https://x.com/alice/status/111", archiveMode: "both", backfill: false },
        { kind: "tweet_link", input: "https://x.com/bob/status/222", archiveMode: "both", backfill: false },
      ],
    }));
  });

  it("creates thread, bookmarks, search jobs and cron schedules with filter rules", async () => {
    const user = userEvent.setup();
    vi.mocked(api.createJob).mockResolvedValue({
      id: 10, kind: "thread", status: "pending", input: "", title: "", progress: 0, message: "", createdAt: "", updatedAt: "",
    });
    vi.mocked(api.createArchiveSchedule).mockResolvedValue({
      id: 20, name: "Daily bookmarks", enabled: true, intervalMinutes: 360, cronExpr: "0 3 * * *", timezone: "UTC",
      items: [], nextRunAt: "", lastJobIds: [], createdAt: "", updatedAt: "",
    });

    const { unmount } = renderModal();
    await user.click(screen.getByRole("tab", { name: /长串归档/ }));
    await user.type(screen.getByRole("textbox", { name: "长串推文链接" }), "https://x.com/alice/status/555");
    await user.click(screen.getByRole("button", { name: /立即下载/ }));
    await waitFor(() => expect(api.createJob).toHaveBeenCalledWith("thread", "https://x.com/alice/status/555", "长串归档", { archiveMode: "both", backfill: false }));
    unmount();

    renderModal({ initialKind: "schedule" });
    await user.click(screen.getByRole("tab", { name: /书签归档/ }));
    await user.click(screen.getByRole("button", { name: /归档过滤规则/ }));
    await user.click(screen.getByRole("checkbox", { name: "跳过转推" }));
    await user.click(screen.getByRole("radio", { name: "Cron" }));
    await user.click(screen.getByRole("button", { name: /保存定时计划/ }));

    await waitFor(() => expect(api.createArchiveSchedule).toHaveBeenCalledWith(expect.objectContaining({
      cronExpr: "0 3 * * *",
      items: [expect.objectContaining({
        kind: "bookmarks",
        input: "all",
        filter: { excludeRetweets: true },
      })],
    })));
  });

  it("renders X Article, Poll, Metrics, ALT text badge, and organize actions on TweetCard", async () => {
    const user = userEvent.setup();
    const onToggleStar = vi.fn();
    const onOpenThread = vi.fn();
    const tweet: ArchivedTweet = {
      id: "900",
      url: "https://x.com/alice/status/900",
      text: "Check out this article & poll",
      createdAt: "2026-11-15T12:00:00Z",
      archivedAt: "2026-11-15T12:05:00Z",
      conversationId: "900",
      author: { id: "u1", name: "Alice", screenName: "alice" },
      media: [{
        id: "m1",
        type: "photo",
        url: "https://pbs.twimg.com/media/art.jpg",
        previewUrl: "https://pbs.twimg.com/media/art.jpg",
        bestUrl: "https://pbs.twimg.com/media/art.jpg",
        variants: [],
        altText: "Detailed diagram description",
      }],
      downloads: [{
        id: 50,
        jobId: 1,
        tweetId: "900",
        mediaUrl: "https://pbs.twimg.com/media/art.jpg",
        filePath: "/downloads/art.jpg",
        bytes: 2048,
        createdAt: "2026-11-15T12:05:00Z",
      }],
      jobIds: [1],
      bodyStatus: "completed",
      mediaStatus: "completed",
      starred: false,
      tags: [{ id: 1, name: "精选", color: "blue", createdAt: "" }],
      article: { title: "Deep Dive into X Archive", plainText: "Full article body text here." },
      poll: { totalVotes: 100, choices: [{ label: "Go", votes: 70 }, { label: "Rust", votes: 30 }] },
      metrics: { likes: 1200, retweets: 340, replies: 56, views: 25000 },
    };

    renderWithProviders(
      <TweetCard
        tweet={tweet}
        compact={false}
        highlight=""
        onFilterAuthor={vi.fn()}
        onOpenDetail={vi.fn()}
        onOpenMedia={vi.fn()}
        onMoreMedia={vi.fn()}
        onRetry={vi.fn()}
        onToggleStar={onToggleStar}
        onOpenThread={onOpenThread}
        retryPending={false}
      />,
    );

    expect(screen.getByText("Deep Dive into X Archive")).toBeTruthy();
    expect(screen.getByText("Full article body text here.")).toBeTruthy();
    expect(screen.getByText("Go")).toBeTruthy();
    expect(screen.getByText("70% (70)")).toBeTruthy();
    expect(screen.getByText("ALT")).toBeTruthy();
    expect(screen.getByText("精选")).toBeTruthy();
    expect(screen.getByText("1,200")).toBeTruthy();
    expect(screen.getByText("25.0K")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "收藏推文" }));
    expect(onToggleStar).toHaveBeenCalledWith("900", true);
    await user.click(screen.getByRole("button", { name: "查看长串对话" }));
    expect(onOpenThread).toHaveBeenCalledWith("900");
  });
});
