// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { expect, it } from "vitest";
import { renderWithProviders, screen } from "../test/render";
import { SettingsPage } from "./SettingsPage";
import { act, fireEvent } from "@testing-library/react";
import i18n from "../lib/i18n";
import type { AppConfig } from "../lib/api";

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
  fireEvent.keyDown(screen.getByRole("combobox", { name: "界面语言" }), { key: "ArrowDown" });
  await user.click(screen.getByRole("option", { name: "English" }));
  expect(document.documentElement.lang).toBe("en");
  expect(localStorage.getItem("open-xdownload-language")).toBe("en");
  expect(screen.getByRole("textbox", { name: "Network proxy (Proxy)" })).toBe(proxy);
  expect((proxy as HTMLInputElement).value).toBe("http://unsaved:7890");
  expect(screen.getByRole("button", { name: "Save configuration" })).toBeTruthy();
});

it("updates cached cookie import diagnostics when the language changes", async () => {
  const user = userEvent.setup();
  renderWithProviders(<QueryClientProvider client={new QueryClient()}>
    <SettingsPage config={config} />
  </QueryClientProvider>);
  await user.click(screen.getByRole("button", { name: "批量粘贴" }));
  await user.type(screen.getByRole("textbox", { name: "批量粘贴 Cookie 内容" }), "not-a-cookie");
  expect(screen.getByText(/没有识别到账号/)).toBeTruthy();
  await act(() => i18n.changeLanguage("en"));
  expect(screen.getByText(/No accounts found/)).toBeTruthy();
  expect((screen.getByRole("textbox", { name: "Paste cookie batch" }) as HTMLTextAreaElement).value).toBe("not-a-cookie");
});
