import { describe, expect, it } from "vitest";
import type { DownloadRecord, TweetData } from "./api";
import { formatTweetTime, mediaKey, resolveTweetDisplay, splitRelatedDownloads, tokenizeTweetText } from "./tweetDisplay";

function tweet(patch: Partial<TweetData> & Pick<TweetData, "id">): TweetData {
  return {
    url: `https://x.com/user/status/${patch.id}`,
    text: `text ${patch.id}`,
    createdAt: "2026-11-10T12:00:00Z",
    author: { id: "1", name: "User", screenName: "user" },
    media: [],
    ...patch,
  };
}

function download(id: number, mediaUrl: string): DownloadRecord {
  return { id, jobId: 1, tweetId: "1", mediaUrl, filePath: `asset:${id}`, bytes: 1, createdAt: "2026-11-10T12:00:00Z" };
}

describe("tokenizeTweetText", () => {
  it("keeps plain text intact so the body textContent never changes", () => {
    const text = "Long archived body with  spaces\nand a newline";
    expect(tokenizeTweetText(text)).toEqual([{ type: "text", value: text }]);
  });

  it("links URLs, mentions and hashtags without swallowing trailing punctuation", () => {
    const tokens = tokenizeTweetText("See https://t.co/abc?x=1, ask @alice and #归档 now");
    expect(tokens.map(token => token.type)).toEqual(["text", "url", "text", "mention", "text", "hashtag", "text"]);
    expect(tokens[1]).toMatchObject({ value: "https://t.co/abc?x=1", href: "https://t.co/abc?x=1" });
    expect(tokens[3]).toMatchObject({ value: "@alice", href: "https://x.com/alice" });
    expect(tokens[5]).toMatchObject({ value: "#归档", href: `https://x.com/hashtag/${encodeURIComponent("归档")}` });
    expect(tokens.map(token => token.value).join("")).toBe("See https://t.co/abc?x=1, ask @alice and #归档 now");
  });

  it("ignores email addresses and numeric-only hashtags", () => {
    expect(tokenizeTweetText("mail me at bob@example.com #2026")).toEqual([{ type: "text", value: "mail me at bob@example.com #2026" }]);
  });

  it("tags uppercase HTTP/HTTPS URLs as url tokens rather than hashtags", () => {
    const tokens = tokenizeTweetText("Visit HTTPS://EXAMPLE.COM/path and HTTP://t.co/abc");
    expect(tokens.filter(token => token.type !== "text")).toEqual([
      { type: "url", value: "HTTPS://EXAMPLE.COM/path", href: "HTTPS://EXAMPLE.COM/path" },
      { type: "url", value: "HTTP://t.co/abc", href: "HTTP://t.co/abc" },
    ]);
  });
});

describe("formatTweetTime", () => {
  it("formats recent timestamps as short X-style relative times", () => {
    const now = Date.parse("2026-11-17T12:00:00Z");
    expect(formatTweetTime("2026-11-17T11:59:30Z", now)).toMatch(/^(30s|30秒)$/);
    expect(formatTweetTime("2026-11-17T11:55:00Z", now)).toMatch(/^(5m|5分钟)$/);
    expect(formatTweetTime("2026-11-17T09:00:00Z", now)).toMatch(/^(3h|3小时)$/);
  });

  it("falls back to a calendar date and includes the year when it differs", () => {
    const now = Date.parse("2026-11-17T12:00:00Z");
    expect(formatTweetTime("2026-03-02T00:00:00Z", now)).toMatch(/3月2日|Mar 2/);
    expect(formatTweetTime("2025-03-02T00:00:00Z", now)).toMatch(/2025年3月2日|Mar 2, 2025/);
  });
});

describe("media identity", () => {
  it("ignores image size tags and video quality variants", () => {
    expect(mediaKey("https://pbs.twimg.com/media/ABC123?format=jpg&name=900x900")).toBe("media/ABC123");
    expect(mediaKey("https://pbs.twimg.com/media/ABC123.jpg")).toBe("media/ABC123");
    expect(mediaKey("https://video.twimg.com/ext_tw_video/123/pu/vid/1280x720/a.mp4?tag=12")).toBe("ext_tw_video/123");
    expect(mediaKey("https://video.twimg.com/ext_tw_video/123/pu/vid/640x360/b.mp4")).toBe("ext_tw_video/123");
  });

  it("moves downloads that belong to the quoted tweet into its card", () => {
    const quote = tweet({ id: "2", media: [{ id: "m", type: "photo", url: "https://pbs.twimg.com/media/Q?format=jpg", previewUrl: "", bestUrl: "", variants: [] }] });
    const result = splitRelatedDownloads([
      download(1, "https://pbs.twimg.com/media/MAIN?format=jpg"),
      download(2, "https://pbs.twimg.com/media/Q?format=jpg&name=small"),
    ], [quote]);
    expect(result.main.map(record => record.id)).toEqual([1]);
    expect(result.byRelated.get("2")?.map(record => record.id)).toEqual([2]);
  });
});

describe("resolveTweetDisplay", () => {
  it("shows a repost through its archived original and keeps other context as quotes", () => {
    const original = tweet({ id: "2", author: { id: "2", name: "Bob", screenName: "bob" }, text: "Original" });
    const extra = tweet({ id: "3", text: "Extra context" });
    const outer = tweet({ id: "1", text: "RT @bob: Original", retweetedTweetId: "2", quotedTweetId: "2", related: [original, extra] });
    const display = resolveTweetDisplay(outer);
    expect(display.tweet.id).toBe("2");
    expect(display.repostedBy).toEqual({ name: "User", screenName: "user" });
    expect(display.quotes.map(item => item.id)).toEqual(["3"]);
    expect(display.missingRepostId).toBeUndefined();
    expect(display.missingQuoteId).toBeUndefined();
  });

  it("resolves reply parents and reports unarchived quotes", () => {
    const parent = tweet({ id: "2", text: "Parent" });
    const outer = tweet({ id: "1", replyToId: "2", quotedTweetId: "9", related: [parent] });
    const display = resolveTweetDisplay(outer);
    expect(display.replyTo?.id).toBe("2");
    expect(display.replyToId).toBeUndefined();
    expect(display.missingQuoteId).toBe("9");
  });

  it("reports a missing repost original instead of rendering the RT prefix", () => {
    const display = resolveTweetDisplay(tweet({ id: "1", text: "RT @bob: Gone", retweetedTweetId: "2" }));
    expect(display.tweet.text).toBe("RT @bob: Gone");
    expect(display.missingRepostId).toBe("2");
  });
});
