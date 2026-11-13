// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { expect, it, vi } from "vitest";
import { renderWithProviders, screen } from "../test/render";
import { SettingsPage } from "./SettingsPage";
import { act, waitFor } from "@testing-library/react";
import i18n from "../lib/i18n";
import type { AppConfig } from "../lib/api";

vi.mock("../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../lib/api")>(),
  getXAccounts: vi.fn().mockResolvedValue({ scraper: "legacy", accounts: [] }),
}));

const config: AppConfig = {
  downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "",
  autoRetryFailed: true, autoFollowProtected: false, includeNestedTweetMedia: false,
  incrementalArchive: false, fileNamingMode: "tweet_text", maxFilenameLength: 120,
  storageType: "local",
};

it("switches settings to English without losing the unsaved configuration draft", async () => {
  const user = userEvent.setup();
  renderWithProviders(<QueryClientProvider client={new QueryClient()}>
    <SettingsPage config={config} />
  </QueryClientProvider>);
  const proxy = screen.getByRole("textbox", { name: "网络代理 (Proxy)" });
  await user.type(proxy, "http://unsaved:7890");
  await user.click(screen.getByRole("combobox", { name: "界面语言" }));
  await user.click(screen.getByRole("option", { name: "English" }));
  await waitFor(() => expect(document.documentElement.lang).toBe("en"));
  expect(localStorage.getItem("open-xdownload-language")).toBe("en");
  expect(screen.getByRole("textbox", { name: "Network proxy (Proxy)" })).toBe(proxy);
  expect((proxy as HTMLInputElement).value).toBe("http://unsaved:7890");
  expect(screen.getByRole("button", { name: "Save configuration" })).toBeTruthy();
});

it("translates the X account pool section when the language changes", async () => {
  renderWithProviders(<QueryClientProvider client={new QueryClient()}>
    <SettingsPage config={config} />
  </QueryClientProvider>);
  expect(await screen.findByRole("heading", { name: "X 账号池" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "导入账号" })).toBeTruthy();
  await act(() => i18n.changeLanguage("en"));
  expect(screen.getByRole("heading", { name: "X account pool" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Import account" })).toBeTruthy();
});
