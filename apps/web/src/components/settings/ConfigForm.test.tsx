// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import { act, renderWithProviders, screen, waitFor } from "../../test/render";
import { ConfigForm } from "./ConfigForm";
import { TooltipProvider } from "../ui/Overlay";

vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  updateConfig: vi.fn(),
  checkAuth: vi.fn().mockResolvedValue({ ok: true, message: "ok" }),
}));

vi.mock("./StorageSettings", () => ({ StorageSettings: () => null }));

it("keeps edits made while a configuration save is pending", async () => {
  const config: api.AppConfig = {
    downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "",
    autoRetryFailed: true, autoFollowProtected: false, includeNestedTweetMedia: false,
    incrementalArchive: false, fileNamingMode: "tweet_text", maxFilenameLength: 120,
    storageType: "local",
  };
  let resolveSave!: (updated: api.AppConfig) => void;
  vi.mocked(api.updateConfig).mockImplementation(() => new Promise((resolve) => { resolveSave = resolve; }));
  const user = userEvent.setup();
  renderWithProviders(
    <QueryClientProvider client={new QueryClient()}><ConfigForm config={config} /></QueryClientProvider>,
  );
  const input = screen.getByPlaceholderText("http://127.0.0.1:7890 或 socks5://127.0.0.1:1080");
  await user.type(input, "http://first:7890");
  await user.click(screen.getByRole("button", { name: "保存配置" }));
  await waitFor(() => expect(api.updateConfig).toHaveBeenCalled());
  await user.clear(input);
  await user.type(input, "http://second:7890");

  await act(async () => resolveSave({ ...config, proxyUrl: "http://first:7890" }));

  await waitFor(() => expect((input as HTMLInputElement).value).toBe("http://second:7890"));
});

it("associates settings controls with their visible labels", () => {
  const config: api.AppConfig = {
    downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "",
    autoRetryFailed: true, autoFollowProtected: false, includeNestedTweetMedia: false,
    incrementalArchive: false, fileNamingMode: "tweet_text", maxFilenameLength: 120,
    storageType: "local",
  };
  renderWithProviders(
    <QueryClientProvider client={new QueryClient()}><ConfigForm config={config} /></QueryClientProvider>,
  );

  expect(screen.getByRole("textbox", { name: "网络代理 (Proxy)" })).not.toBeNull();
  expect(screen.getByRole("spinbutton", { name: "最大文件名长度" })).not.toBeNull();
  expect(screen.getByRole("combobox", { name: "文件命名策略" })).not.toBeNull();
  expect(screen.queryByRole("group", { name: "明暗主题模式" })).toBeNull();
  expect(screen.queryByRole("group", { name: "主题强调色" })).toBeNull();
});

it("syncs the latest GET config when a saved response cannot be confirmed", async () => {
  const config: api.AppConfig = {
    downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "",
    autoRetryFailed: true, autoFollowProtected: false, includeNestedTweetMedia: false,
    incrementalArchive: false, fileNamingMode: "tweet_text", maxFilenameLength: 120,
    storageType: "local",
  };
  vi.mocked(api.updateConfig).mockResolvedValue({ ...config, proxyUrl: "http://saved:7890" });
  const client = new QueryClient();
  const form = (value: api.AppConfig) => <QueryClientProvider client={client}><ConfigForm config={value} /></QueryClientProvider>;
  const view = renderWithProviders(form(config));
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "保存配置" }));
  const input = screen.getByRole("textbox", { name: "网络代理 (Proxy)" }) as HTMLInputElement;
  await waitFor(() => expect(input.value).toBe("http://saved:7890"));

  view.rerender(<TooltipProvider>{form({ ...config, proxyUrl: "http://environment:7890" })}</TooltipProvider>);

  await waitFor(() => expect(input.value).toBe("http://environment:7890"), { timeout: 4_000 });
});

it("asks for confirmation before discarding a dirty draft on reload", async () => {
  const config: api.AppConfig = {
    downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "",
    autoRetryFailed: true, autoFollowProtected: false, includeNestedTweetMedia: false,
    incrementalArchive: false, fileNamingMode: "tweet_text", maxFilenameLength: 120,
    storageType: "local",
  };
  const onRefresh = vi.fn();
  renderWithProviders(<QueryClientProvider client={new QueryClient()}><ConfigForm config={config} onRefresh={onRefresh} /></QueryClientProvider>);
  const user = userEvent.setup();
  const input = screen.getByRole("textbox", { name: "网络代理 (Proxy)" }) as HTMLInputElement;
  await user.type(input, "http://unsaved:7890");

  // dirty 状态下点重新加载 → 先出确认气泡，不立即丢弃
  await user.click(screen.getByRole("button", { name: "重新加载配置" }));
  expect(onRefresh).not.toHaveBeenCalled();
  expect(screen.getByText("放弃未保存的修改？")).not.toBeNull();
  expect(input.value).toBe("http://unsaved:7890");

  // 取消 → 草稿保留
  await user.click(screen.getByRole("button", { name: "取消" }));
  expect(onRefresh).not.toHaveBeenCalled();
  expect(input.value).toBe("http://unsaved:7890");

  // 再次打开并确认 → 丢弃草稿并刷新
  await user.click(screen.getByRole("button", { name: "重新加载配置" }));
  await user.click(screen.getByRole("button", { name: "重新加载" }));
  expect(onRefresh).toHaveBeenCalledOnce();
  expect(input.value).toBe("");
});

it("reloads immediately when the draft is clean", async () => {
  const config: api.AppConfig = {
    downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "",
    autoRetryFailed: true, autoFollowProtected: false, includeNestedTweetMedia: false,
    incrementalArchive: false, fileNamingMode: "tweet_text", maxFilenameLength: 120,
    storageType: "local",
  };
  const onRefresh = vi.fn();
  renderWithProviders(<QueryClientProvider client={new QueryClient()}><ConfigForm config={config} onRefresh={onRefresh} /></QueryClientProvider>);
  const user = userEvent.setup();

  await user.click(screen.getByRole("button", { name: "重新加载配置" }));

  // 没有未保存修改时不该弹确认
  expect(screen.queryByText("放弃未保存的修改？")).toBeNull();
  expect(onRefresh).toHaveBeenCalledOnce();
});

it("labels fields that are overridden by environment variables", () => {
  // envOverridden 是 GET /api/config 的可选元数据（camelCase 字段名）
  const config = {
    downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "",
    autoRetryFailed: true, autoFollowProtected: false, includeNestedTweetMedia: false,
    incrementalArchive: false, fileNamingMode: "tweet_text", maxFilenameLength: 120,
    storageType: "local",
    envOverridden: ["proxyUrl", "authToken", "csrfToken", "additionalCookies"],
  } as api.AppConfig;
  renderWithProviders(
    <QueryClientProvider client={new QueryClient()}><ConfigForm config={config} /></QueryClientProvider>,
  );

  // 四处都应有标注（代理 + 两个主 Cookie 字段 + 备用 Cookie 池）；downloadDir 没被覆盖，不显示
  expect(screen.getAllByText("由环境变量提供，保存不会覆盖")).toHaveLength(4);
});

it("does not label anything when envOverridden is absent", () => {
  const config: api.AppConfig = {
    downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "",
    autoRetryFailed: true, autoFollowProtected: false, includeNestedTweetMedia: false,
    incrementalArchive: false, fileNamingMode: "tweet_text", maxFilenameLength: 120,
    storageType: "local",
  };
  renderWithProviders(
    <QueryClientProvider client={new QueryClient()}><ConfigForm config={config} /></QueryClientProvider>,
  );
  expect(screen.queryByText("由环境变量提供，保存不会覆盖")).toBeNull();
});
