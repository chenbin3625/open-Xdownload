// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { beforeEach, expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import { renderWithProviders, screen, waitFor } from "../../test/render";
import { FailedTweetQueue } from "./FailedTweetQueue";

vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  getFailedTweets: vi.fn(),
  deleteFailedTweet: vi.fn(),
}));

function makeItem(id: number): api.FailedTweet {
  return {
    id,
    tweetId: `100${id}`,
    jobId: 1,
    entityId: 1,
    jobTitle: `Job ${id}`,
    entityName: "alice",
    entityParentDir: "/downloads",
    userId: "u1",
    userScreenName: "alice",
    userName: "Alice",
    error: "timeout",
    createdAt: "2026-11-01T00:00:00Z",
    updatedAt: "2026-11-01T00:00:00Z",
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

it("clamps page when totalPages shrinks and disables only the deleting row", async () => {
  let resolveDelete!: (value: { ok: boolean }) => void;
  vi.mocked(api.deleteFailedTweet).mockImplementation(() => new Promise<{ ok: boolean }>(r => { resolveDelete = r; }));
  vi.mocked(api.getFailedTweets).mockImplementation(async (params) => {
    if ((params?.page ?? 1) === 1) {
      return {
        items: [makeItem(1), makeItem(2)],
        pagination: { page: 1, pageSize: 10, total: 20, totalPages: 2 },
      };
    }
    return {
      items: [makeItem(11), makeItem(12)],
      pagination: { page: 2, pageSize: 10, total: 20, totalPages: 2 },
    };
  });

  const user = userEvent.setup();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(
    <QueryClientProvider client={client}>
      <FailedTweetQueue total={20} />
    </QueryClientProvider>,
  );

  await screen.findByText("Job 1");
  await user.click(await screen.findByRole("button", { name: "第 2 页" }));
  await screen.findByText("Job 11");

  const deleteBtns = screen.getAllByRole("button", { name: "删除此条记录" });
  await user.click(deleteBtns[0]);
  await user.click(screen.getByRole("button", { name: "删除" }));
  await waitFor(() => expect(api.deleteFailedTweet).toHaveBeenCalledWith(11, expect.anything()));
  await waitFor(() => expect(screen.getAllByRole("button", { name: "删除此条记录" })[0].hasAttribute("disabled")).toBe(true));
  expect(screen.getAllByRole("button", { name: "删除此条记录" })[1].hasAttribute("disabled")).toBe(false);

  // Shrink totalPages to 1 on next fetch and resolve delete
  vi.mocked(api.getFailedTweets).mockResolvedValue({
    items: [makeItem(1)],
    pagination: { page: 1, pageSize: 10, total: 1, totalPages: 1 },
  });
  resolveDelete({ ok: true });
  await waitFor(() => expect(screen.getByText("Job 1")).toBeTruthy());
});

it("moves back from an empty last page when deleting the sole item on page 2", async () => {
  vi.mocked(api.deleteFailedTweet).mockResolvedValue({ ok: true });
  let deleted = false;
  vi.mocked(api.getFailedTweets).mockImplementation(async (params) => {
    const requestedPage = params?.page ?? 1;
    if (requestedPage === 1) {
      return {
        items: [makeItem(1)],
        pagination: { page: 1, pageSize: 10, total: deleted ? 10 : 11, totalPages: deleted ? 1 : 2 },
      };
    }
    return {
      items: deleted ? [] : [makeItem(11)],
      pagination: { page: 2, pageSize: 10, total: deleted ? 10 : 11, totalPages: deleted ? 1 : 2 },
    };
  });

  const user = userEvent.setup();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(
    <QueryClientProvider client={client}>
      <FailedTweetQueue total={11} />
    </QueryClientProvider>,
  );

  await screen.findByText("Job 1");
  await user.click(await screen.findByRole("button", { name: "第 2 页" }));
  await screen.findByText("Job 11");

  deleted = true;
  await user.click(screen.getByRole("button", { name: "删除此条记录" }));
  await user.click(screen.getByRole("button", { name: "删除" }));

  await waitFor(() => expect(screen.getByText("Job 1")).toBeTruthy());
});
