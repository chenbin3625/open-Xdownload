// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { expect, it } from "vitest";
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
