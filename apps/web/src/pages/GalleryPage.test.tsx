// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { describe, expect, it } from "vitest";
import { TooltipProvider } from "../components/ui/Overlay";
import type { DownloadRecord } from "../lib/api";
import { GalleryPage } from "./GalleryPage";

const records: DownloadRecord[] = [
  {
    id: 1, jobId: 1, tweetId: "original", mediaUrl: "https://pbs.twimg.com/media/shared.jpg",
    filePath: "/downloads/shared.jpg", bytes: 100, createdAt: "2026-09-01T00:00:00Z",
    userScreenName: "alice", userName: "Alice",
  },
  {
    id: 2, jobId: 2, tweetId: "retweet", mediaUrl: "https://pbs.twimg.com/media/shared?format=jpg",
    filePath: "/downloads/shared.jpg", bytes: 100, createdAt: "2026-09-02T00:00:00Z",
    userScreenName: "bob", userName: "Bob",
  },
  {
    id: 3, jobId: 3, tweetId: "other", mediaUrl: "https://pbs.twimg.com/media/other.jpg",
    filePath: "/downloads/other.jpg", bytes: 90, createdAt: "2026-09-03T00:00:00Z",
  },
];

it("shows one item per saved file while searching associated retweets", async () => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider><GalleryPage downloads={records} /></TooltipProvider>
    </QueryClientProvider>,
  );
  expect(screen.getByText("全部 (2)")).toBeTruthy();
  expect(screen.getAllByText("shared.jpg")).toHaveLength(1);

  await userEvent.type(screen.getByPlaceholderText("搜索文件名、推文 ID..."), "retweet");
  expect(screen.getAllByText("shared.jpg")).toHaveLength(1);
  expect(screen.queryByText("other.jpg")).toBeNull();
});

const mediaRecords: DownloadRecord[] = [
  {
    id: 11, jobId: 1, tweetId: "v1", mediaUrl: "https://video.twimg.com/a.mp4",
    filePath: "/downloads/clip-a.mp4", bytes: 100, createdAt: "2026-09-01T00:00:00Z",
  },
  {
    id: 12, jobId: 1, tweetId: "v2", mediaUrl: "https://video.twimg.com/b.mp4",
    filePath: "/downloads/clip-b.mp4", bytes: 100, createdAt: "2026-09-02T00:00:00Z",
  },
];

function renderGallery(items: DownloadRecord[]) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider><GalleryPage downloads={items} /></TooltipProvider>
    </QueryClientProvider>,
  );
}

function previewedFile() {
  return document.querySelector("[role=dialog] video, [role=dialog] img")?.getAttribute("src");
}

function pressArrow(target: EventTarget) {
  const event = new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true });
  target.dispatchEvent(event);
  return event;
}

describe("lightbox arrow keys", () => {
  it("steps to the next item when nothing interactive is focused", async () => {
    renderGallery(mediaRecords);
    await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
    expect(previewedFile()).toBe("/api/library/downloads/11/file");

    const event = pressArrow(document.body);

    expect(event.defaultPrevented).toBe(true);
    await waitFor(() => expect(previewedFile()).toBe("/api/library/downloads/12/file"));
  });

  it("leaves arrow keys to a focused video so it can seek", async () => {
    renderGallery(mediaRecords);
    await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
    const video = document.querySelector("[role=dialog] video");
    expect(video).not.toBeNull();

    const event = pressArrow(video!);

    expect(event.defaultPrevented).toBe(false);
    expect(previewedFile()).toBe("/api/library/downloads/11/file");
  });

  it("ignores arrow keys typed into text fields", async () => {
    renderGallery(mediaRecords);
    await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
    const input = document.createElement("input");
    document.body.appendChild(input);

    const event = pressArrow(input);

    expect(event.defaultPrevented).toBe(false);
    expect(previewedFile()).toBe("/api/library/downloads/11/file");
    input.remove();
  });
});

it("ignores arrow keys inside contenteditable regions", async () => {
  renderGallery(mediaRecords);
  await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
  const editor = document.createElement("div");
  editor.setAttribute("contenteditable", "true");
  const inner = document.createElement("span");
  editor.appendChild(inner);
  document.body.appendChild(editor);

  const event = pressArrow(inner);

  expect(event.defaultPrevented).toBe(false);
  expect(previewedFile()).toBe("/api/library/downloads/11/file");
  editor.remove();
});
