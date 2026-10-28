// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import { renderWithProviders, screen, waitFor } from "../../test/render";
import { LocalDirectoryPicker } from "./LocalDirectoryPicker";
import { TooltipProvider } from "../ui/Overlay";

vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  listLocalDirectories: vi.fn(),
}));

it("shows changed directory entries after refreshing the same path", async () => {
  const initial: api.LocalDirectoryListing = {
    path: "/downloads", parent: "/", entries: [{ name: "first", path: "/downloads/first", hasChildren: false }],
  };
  vi.mocked(api.listLocalDirectories).mockResolvedValueOnce(initial).mockResolvedValue({
    ...initial, entries: [{ name: "second", path: "/downloads/second", hasChildren: false }],
  });
  renderWithProviders(
    <QueryClientProvider client={new QueryClient()}><LocalDirectoryPicker path="/downloads" onSelect={() => {}} /></QueryClientProvider>,
  );
  await waitFor(() => expect(screen.getByText("first")).toBeTruthy());

  await userEvent.click(screen.getByRole("button", { name: "刷新目录" }));

  await waitFor(() => expect(screen.getByText("second")).toBeTruthy());
  expect(screen.queryByText("first")).toBeNull();
});

it("gives the directory input an accessible name", async () => {
  vi.mocked(api.listLocalDirectories).mockResolvedValue({
    path: "/downloads", parent: "/", entries: [],
  });
  renderWithProviders(
    <QueryClientProvider client={new QueryClient()}><LocalDirectoryPicker path="/downloads" onSelect={() => {}} /></QueryClientProvider>,
  );

  await waitFor(() => expect(screen.getByRole("textbox", { name: "下载目录" })).not.toBeNull());
});

it("clears the previous tree while a new directory fails to load", async () => {
  const initial: api.LocalDirectoryListing = {
    path: "/downloads", parent: "/", entries: [{ name: "first", path: "/downloads/first", hasChildren: false }],
  };
  vi.mocked(api.listLocalDirectories).mockReset().mockImplementation((requestedPath) =>
    requestedPath === "/downloads"
      ? Promise.resolve(initial)
      : Promise.reject(new Error("permission denied")),
  );
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const { rerender } = renderWithProviders(
    <QueryClientProvider client={queryClient}><LocalDirectoryPicker path="/downloads" onSelect={() => {}} /></QueryClientProvider>,
  );
  await waitFor(() => expect(screen.getByText("first")).toBeTruthy());

  rerender(
    <TooltipProvider><QueryClientProvider client={queryClient}><LocalDirectoryPicker path="/private" onSelect={() => {}} /></QueryClientProvider></TooltipProvider>,
  );

  await waitFor(() => expect(screen.getByText("读取目录失败")).toBeTruthy());
  expect(screen.queryByText("first")).toBeNull();
});
