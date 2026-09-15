// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "../components/ui/Overlay";
import * as api from "../lib/api";
import { GalleryPage } from "./GalleryPage";

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return {
    ...actual,
    getLibraryDownloads: vi.fn(),
    getPosterBackfillStatus: vi.fn(),
    cleanupLibraryDownloads: vi.fn(),
  };
});

function renderWithClient(ui: React.ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
    },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>{ui}</TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("GalleryPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getLibraryDownloads).mockResolvedValue([]);
    vi.mocked(api.getPosterBackfillStatus).mockResolvedValue({
      running: false,
      total: 0,
      done: 0,
      fetched: 0,
      skipped: 0,
      failed: 0,
    });
  });

  it("点击检测清理按钮后清理缺失与重复媒体记录", async () => {
    const user = userEvent.setup();
    vi.mocked(api.cleanupLibraryDownloads).mockResolvedValue({
      scanned: 12,
      missingRecords: 2,
      duplicateRecords: 3,
      duplicateFiles: 3,
      bytesFreed: 1048576,
    });

    renderWithClient(<GalleryPage />);

    await user.click(await screen.findByRole("button", { name: "检测清理" }));

    expect(api.cleanupLibraryDownloads).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      expect(screen.getByText("已清理缺失记录 2 条、重复记录 3 条")).not.toBeNull();
    });
  });
});
