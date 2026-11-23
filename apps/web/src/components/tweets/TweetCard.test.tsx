// @vitest-environment jsdom
import React from "react";
import { expect, it, vi } from "vitest";
import i18n from "../../lib/i18n";
import type { ArchivedTweet } from "../../lib/archive";
import { act, renderWithProviders, screen } from "../../test/render";
import { TweetCard } from "./TweetCard";

function makeTweet(overrides: Partial<ArchivedTweet> = {}): ArchivedTweet {
  return {
    id: "1",
    url: "https://x.com/alice/status/1",
    text: "hello",
    createdAt: "2026-11-01T00:00:00Z",
    archivedAt: "2026-11-01T01:00:00Z",
    author: { id: "u1", screenName: "alice", name: "Alice" },
    media: [],
    downloads: [],
    jobIds: [],
    bodyStatus: "completed",
    mediaStatus: "failed",
    ...overrides,
  };
}

function renderCard(tweet: ArchivedTweet, compact = false) {
  return renderWithProviders(
    <TweetCard
      tweet={tweet}
      compact={compact}
      highlight=""
      onFilterAuthor={vi.fn()}
      onOpenDetail={vi.fn()}
      onOpenMedia={vi.fn()}
      onMoreMedia={vi.fn()}
      onRetry={vi.fn()}
      retryPending={false}
    />,
  );
}

it("omits/disables invalid original URLs, sets no-referrer on remote photo previews, and checks quote media for retryable", async () => {
  const prevLang = i18n.language;
  const tweet = makeTweet({
    url: "javascript:alert(1)",
    quotedTweetId: "2",
    related: [
      {
        id: "2",
        url: "https://x.com/bob/status/2",
        text: "quoted",
        createdAt: "2026-11-01T00:00:00Z",
        author: { id: "u2", screenName: "bob", name: "Bob" },
        media: [{
          id: "m1",
          type: "photo",
          url: "https://pbs.twimg.com/media/abc.jpg",
          previewUrl: "https://pbs.twimg.com/media/abc.jpg",
          bestUrl: "https://pbs.twimg.com/media/abc.jpg",
          variants: [],
        }],
      },
    ],
    downloads: [
      {
        id: 10,
        jobId: 1,
        tweetId: "2",
        mediaUrl: "https://pbs.twimg.com/media/abc.jpg",
        filePath: "/downloads/abc.jpg",
        bytes: 1024,
        createdAt: "2026-11-01T01:00:00Z",
        fileUrl: "/api/library/downloads/10/file",
        previewUrl: "https://pbs.twimg.com/media/abc.jpg",
      },
    ],
  });

  try {
    renderCard(tweet);

    // Invalid javascript: URL should not render an anchor
    expect(screen.queryByRole("link", { name: "原始推文" })).toBeNull();
    expect(screen.getByRole("button", { name: "原始推文" }).hasAttribute("disabled")).toBe(true);

    // Remote photo preview uses no-referrer
    const img = screen.getByRole("img", { name: "bob · u2" });
    expect(img.getAttribute("referrerpolicy")).toBe("no-referrer");

    // Retry button appears because quoted tweet has media
    expect(screen.getByRole("button", { name: "重试媒体" })).toBeTruthy();

    // Language switch updates memoized TweetCard labels
    await act(async () => {
      await i18n.changeLanguage("en");
    });
    expect(screen.getByRole("button", { name: "Retry media" })).toBeTruthy();
  } finally {
    await act(async () => {
      await i18n.changeLanguage(prevLang);
    });
  }
});

it("displays display.repostedBy.screenName in the repost banner instead of the original author", () => {
  const tweet = makeTweet({
    id: "rt-1",
    text: "RT @orig_author: original body",
    author: { id: "u-reposter", screenName: "reposter_handle", name: "Reposter Name" },
    retweetedTweetId: "orig-1",
    related: [
      {
        id: "orig-1",
        url: "https://x.com/orig_author/status/orig-1",
        text: "original body",
        createdAt: "2026-11-01T00:00:00Z",
        author: { id: "u-orig", screenName: "orig_author", name: "Original Author" },
        media: [],
      },
    ],
  });

  renderCard(tweet);
  expect(screen.getByText(/reposter_handle/)).toBeTruthy();
  expect(screen.getByText("Original Author")).toBeTruthy();
});

it("blocks unsafe media src protocols, sets no-referrer on remote thumbnails/images, and shows overflow badge in QuoteCard", () => {
  const quoteMedia = [1, 2, 3, 4].map((index) => ({
    id: `qm${index}`,
    type: "photo" as const,
    url: `https://pbs.twimg.com/media/q${index}.jpg`,
    previewUrl: `https://pbs.twimg.com/media/q${index}.jpg`,
    bestUrl: `https://pbs.twimg.com/media/q${index}.jpg`,
    variants: [],
  }));
  const quoteDownloads = [1, 2, 3, 4].map((index) => ({
    id: 100 + index,
    jobId: 1,
    tweetId: "q1",
    mediaUrl: `https://pbs.twimg.com/media/q${index}.jpg`,
    filePath: `/downloads/q${index}.jpg`,
    bytes: 512,
    createdAt: "2026-11-01T01:00:00Z",
    fileUrl: `/api/library/downloads/${100 + index}/file`,
    previewUrl: `https://pbs.twimg.com/media/q${index}.jpg`,
  }));

  const tweet = makeTweet({
    id: "main-1",
    quotedTweetId: "q1",
    related: [
      {
        id: "q1",
        url: "https://x.com/bob/status/q1",
        text: "quoted with 4 photos",
        createdAt: "2026-11-01T00:00:00Z",
        author: { id: "u2", screenName: "bob", name: "Bob" },
        media: quoteMedia,
      },
    ],
    downloads: [
      {
        id: 20,
        jobId: 1,
        tweetId: "main-1",
        mediaUrl: "javascript:alert(1)",
        filePath: "/downloads/bad.jpg",
        bytes: 100,
        createdAt: "2026-11-01T01:00:00Z",
        fileUrl: "javascript:alert(1)",
        previewUrl: "data:text/html,<script>alert(1)</script>",
        assetUrls: { thumbnail: "//evil.example/x.jpg" },
      },
      {
        id: 21,
        jobId: 1,
        tweetId: "main-1",
        mediaUrl: "https://cdn.example/video.mp4",
        filePath: "/downloads/remote.mp4",
        bytes: 100,
        createdAt: "2026-11-01T01:00:00Z",
        fileUrl: "/api/library/downloads/21/file",
        assetUrls: { thumbnail: "https://cdn.example/thumb.jpg" },
      },
      {
        id: 22,
        jobId: 1,
        tweetId: "main-1",
        mediaUrl: "https://cdn.example/photo.jpg",
        filePath: "/downloads/remote.jpg",
        bytes: 100,
        createdAt: "2026-11-01T01:00:00Z",
        fileUrl: "https://cdn.example/photo.jpg",
      },
      ...quoteDownloads,
    ],
  });

  // Non-compact renders all 3 main items and 3 of 4 quoted items (+1 overflow badge)
  renderCard(tweet, false);

  const imgs = Array.from(document.querySelectorAll("img"));
  // Unsafe protocol media must never appear in any img[src]
  for (const img of imgs) {
    const src = img.getAttribute("src") ?? "";
    expect(src).not.toMatch(/^(javascript:|data:|\/\/)/i);
  }

  // Remote video poster thumbnail must carry no-referrer
  const remoteThumb = imgs.find(img => img.getAttribute("src") === "https://cdn.example/thumb.jpg");
  expect(remoteThumb).toBeTruthy();
  expect(remoteThumb?.getAttribute("referrerpolicy")).toBe("no-referrer");

  // Remote photo image must carry no-referrer
  const remotePhoto = imgs.find(img => img.getAttribute("src") === "https://cdn.example/photo.jpg");
  expect(remotePhoto).toBeTruthy();
  expect(remotePhoto?.getAttribute("referrerpolicy")).toBe("no-referrer");

  // Non-compact QuoteCard renders 3 of 4 items and displays +1 overflow badge
  const quoteBox = document.querySelector(".tweet-quote");
  expect(quoteBox?.querySelector(".tweet-media-count")?.textContent).toBe("+1");
});
