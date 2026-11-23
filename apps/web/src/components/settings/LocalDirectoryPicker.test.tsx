// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import { act, renderWithProviders, screen, waitFor } from "../../test/render";
import { LocalDirectoryPicker } from "./LocalDirectoryPicker";
import { TooltipProvider } from "../semi";

vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  listLocalDirectories: vi.fn(),
  createLocalDirectory: vi.fn(),
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

it("keeps a failed child directory collapsed and preserves loaded children on same-path refetch", async () => {
  const initial: api.LocalDirectoryListing = {
    path: "/downloads", parent: "/", entries: [
      { name: "sub", path: "/downloads/sub", hasChildren: true },
      { name: "bad", path: "/downloads/bad", hasChildren: true },
    ],
  };
  vi.mocked(api.listLocalDirectories).mockReset().mockImplementation((requestedPath) => {
    if (requestedPath === "/downloads") return Promise.resolve(initial);
    if (requestedPath === "/downloads/sub") {
      return Promise.resolve({ path: "/downloads/sub", parent: "/downloads", entries: [{ name: "leaf", path: "/downloads/sub/leaf", hasChildren: false }] });
    }
    return Promise.reject(new Error("forbidden"));
  });
  const user = userEvent.setup();
  renderWithProviders(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><LocalDirectoryPicker path="/downloads" onSelect={() => {}} /></QueryClientProvider>,
  );

  await user.dblClick(await screen.findByText("bad"));
  await waitFor(() => expect(screen.getByText("读取子目录失败")).toBeTruthy());
  expect(screen.getByText("bad").closest("[role=\"treeitem\"]")?.getAttribute("aria-expanded")).toBe("false");

  await user.dblClick(screen.getByText("sub"));
  await waitFor(() => expect(screen.getByText("leaf")).toBeTruthy());

  // Refetch same root path should keep sub expanded AND preserve loaded leaf children
  await user.click(screen.getByRole("button", { name: "刷新目录" }));
  await waitFor(() => expect(api.listLocalDirectories).toHaveBeenCalledWith("/downloads", expect.anything()));
  expect(screen.getByText("sub").closest("[role=\"treeitem\"]")?.getAttribute("aria-expanded")).toBe("true");
  expect(screen.getByText("leaf")).toBeTruthy();
});

it("does not invoke onSelect after unmounting while directory creation is pending", async () => {
  vi.mocked(api.listLocalDirectories).mockResolvedValue({
    path: "/downloads", parent: "/", entries: [],
  });
  let resolveCreate!: (listing: api.LocalDirectoryListing) => void;
  vi.mocked(api.createLocalDirectory).mockImplementation(() => new Promise((resolve) => { resolveCreate = resolve; }));
  const onSelect = vi.fn();
  const user = userEvent.setup();
  const { unmount } = renderWithProviders(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <LocalDirectoryPicker path="/downloads" onSelect={onSelect} />
    </QueryClientProvider>,
  );

  const input = await screen.findByRole("textbox", { name: "下载目录" });
  await waitFor(() => expect(api.listLocalDirectories).toHaveBeenCalled());
  await user.clear(input);
  await user.type(input, "/downloads/new");
  await user.click(screen.getByRole("button", { name: "创建" }));
  await waitFor(() => expect(api.createLocalDirectory).toHaveBeenCalledWith("/downloads/new", expect.anything()));

  unmount();
  await act(async () => {
    resolveCreate({ path: "/downloads/new", parent: "/downloads", entries: [] });
  });
  expect(onSelect).not.toHaveBeenCalled();
});
