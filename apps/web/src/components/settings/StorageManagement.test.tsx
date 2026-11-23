// @vitest-environment jsdom
import React from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { renderWithProviders, screen } from "../../test/render";
import { getStorage, getTweets, migrateStorage, saveStorage, type StorageDocument } from "../../lib/archive";
import { api } from "../../lib/api";
import { StorageManagement } from "./StorageManagement";

vi.mock("../../lib/archive", async original => ({ ...await original<typeof import("../../lib/archive")>(), getStorage: vi.fn(), getTweets: vi.fn(), saveStorage: vi.fn(), migrateStorage: vi.fn() }));
vi.mock("../../lib/api", async original => ({ ...await original<typeof import("../../lib/api")>(), api: vi.fn() }));

const document: StorageDocument = {
  version: 1, enabled: false,
  targets: [{ id: "local", name: "Local", type: "local", version: 1, root: "/downloads" }, { id: "nas", name: "NAS", type: "nas", version: 1, root: "/mnt/archive" }, { id: "spare", name: "Spare", type: "local", version: 1, root: "/spare" }],
  default: { targetId: "local", prefix: "", directoryTemplate: "{author_id}/{tweet_id}", filenameTemplate: "{media_id}.{ext}" },
  rules: { video: { targetId: "nas", prefix: "videos", directoryTemplate: "{year}", filenameTemplate: "{tweet_id}_{index}.{ext}" } },
  thumbnail: { width: 640, height: 640, quality: 82, format: "jpeg" },
};

function renderStorage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    ...renderWithProviders(<QueryClientProvider client={client}><StorageManagement /></QueryClientProvider>),
  };
}
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(getStorage).mockResolvedValue(structuredClone(document));
  vi.mocked(getTweets).mockResolvedValue({ items: [], pagination: { page: 1, pageSize: 1, total: 0, totalPages: 1 } });
});

it("preserves edits made during a save and uses the returned policy version on the next save", async () => {
  let resolveSave!: (document: StorageDocument) => void;
  vi.mocked(saveStorage).mockImplementationOnce(() => new Promise(resolve => { resolveSave = resolve; }));
  vi.mocked(saveStorage).mockImplementationOnce(async submitted => ({ ...submitted, version: 3 }));
  const user = userEvent.setup(); renderStorage();
  await user.click(await screen.findByRole("button", { name: "展开默认规则" }));
  await user.click(await screen.findByRole("combobox", { name: "分类存储策略" }));
  await user.click(screen.getByRole("option", { name: "按用户 / 年 / 月" }));
  await user.click(screen.getByRole("button", { name: "保存存储设置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(1));
  const filename = screen.getByRole("combobox", { name: "命名策略" });
  await user.click(filename);
  await user.click(screen.getByRole("option", { name: "推文编号 + 媒体编号" }));
  await act(async () => resolveSave({ ...vi.mocked(saveStorage).mock.calls[0][0], version: 2 }));
  expect(filename.textContent).toContain("推文编号 + 媒体编号");
  expect(screen.getByText("尚未保存")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "保存存储设置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(2));
  const submitted = vi.mocked(saveStorage).mock.calls[1][0];
  expect(submitted.version).toBe(2); expect(submitted.default.filenameTemplate).toBe("{tweet_id}_{media_id}_{index}.{ext}");
  expect(submitted.enabled).toBe(true);
  expect(submitted.rules.video).toEqual(document.rules.video);
  await waitFor(() => expect(screen.queryByText("尚未保存")).toBeNull());
});

it("uses readable policies without exposing template variables and exposes preview failures", async () => {
  const user = userEvent.setup(); renderStorage();
  await screen.findByRole("button", { name: "展开默认规则" });
  expect(screen.queryByRole("textbox", { name: "目录模板" })).toBeNull();
  expect(screen.queryByRole("textbox", { name: "命名模板" })).toBeNull();
  expect(screen.queryByText(/\{author_id\}/)).toBeNull();
  vi.mocked(api).mockRejectedValueOnce(new Error("Template escaped root"));
  await user.click(screen.getByRole("button", { name: "图片原文件 预览实际路径" }));
  expect(await screen.findByText("Template escaped root")).toBeTruthy();
  expect(screen.getByRole("button", { name: "保存存储设置" }).hasAttribute("disabled")).toBe(true);
});

it("previews each media type inside its own row with the matching media and extension", async () => {
  vi.mocked(api).mockImplementation(async (_path, options) => ({ location: `/downloads/${JSON.parse(options!.body as string).kind}/example` }) as never);
  const user = userEvent.setup(); renderStorage();
  for (const [kind, label, extension] of [["image", "图片原文件", "jpg"], ["video", "视频及 GIF 原文件", "mp4"]]) {
    const row = await screen.findByRole("group", { name: label });
    await user.click(within(row).getByRole("button", { name: `${label} 预览实际路径` }));
    expect(await within(row).findByText(`/downloads/${kind}/example`)).toBeTruthy();
    expect(JSON.parse(vi.mocked(api).mock.calls.at(-1)![1]!.body as string)).toMatchObject({ kind, extension });
  }
});

it("saves folder separation independently and composes filename fields in user order", async () => {
  vi.mocked(saveStorage).mockImplementation(async submitted => ({ ...submitted, version: 2 }));
  const user = userEvent.setup(); renderStorage();
  await user.click(await screen.findByRole("switch", { name: "图片原文件 单独设置" }));
  await user.click(screen.getByRole("switch", { name: "图片原文件 按媒体类型分文件夹" }));
  await user.click(screen.getByRole("combobox", { name: "图片原文件 分类存储策略" }));
  await user.click(screen.getByRole("option", { name: "全部放在同一目录" }));
  await user.click(screen.getByRole("combobox", { name: "图片原文件 命名策略" }));
  await user.click(screen.getByRole("option", { name: "自定义组合" }));
  const add = screen.getByRole("combobox", { name: "图片原文件 添加命名字段" });
  await user.click(add);
  await user.click(screen.getByRole("option", { name: "正文" }));
  await user.click(add);
  await user.click(screen.getByRole("option", { name: "推文编号" }));
  await user.click(screen.getByRole("button", { name: "图片原文件 正文 上移" }));
  await user.click(screen.getByRole("button", { name: "保存存储设置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(1));
  expect(vi.mocked(saveStorage).mock.calls[0][0].rules.image).toMatchObject({ separateFolder: true, directoryTemplate: "", directoryTemplateSet: true, filenameTemplate: "{text}_{media_id}_{tweet_id}.{ext}" });
});

it("can turn off separation from an existing media-kind directory template", async () => {
  vi.mocked(getStorage).mockResolvedValue({ ...structuredClone(document), default: { ...document.default, directoryTemplate: "{kind}/{username}" } });
  vi.mocked(saveStorage).mockImplementation(async submitted => submitted);
  const user = userEvent.setup(); renderStorage();
  await user.click(await screen.findByRole("button", { name: "展开默认规则" }));
  const separate = await screen.findByRole("switch", { name: "按媒体类型分文件夹" });
  expect(separate.getAttribute("aria-checked")).toBe("true");
  await user.click(separate);
  await user.click(screen.getByRole("button", { name: "保存存储设置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(1));
  expect(vi.mocked(saveStorage).mock.calls[0][0].default).toMatchObject({ directoryTemplate: "{username}", separateFolder: false });
});

it("does not show a stale preview after a rule changes", async () => {
  let resolvePreview!: (value: { location: string }) => void;
  vi.mocked(api).mockImplementationOnce(() => new Promise(resolve => { resolvePreview = resolve; }));
  const user = userEvent.setup(); renderStorage();
  await user.click(await screen.findByRole("button", { name: "图片原文件 预览实际路径" }));
  await user.click(screen.getByRole("button", { name: "展开默认规则" }));
  await user.click(screen.getByRole("switch", { name: "按媒体类型分文件夹" }));
  await act(async () => resolvePreview({ location: "/stale/image.jpg" }));
  expect(screen.queryByText("/stale/image.jpg")).toBeNull();
});

it("shows inherited default folder separation in an existing category override", async () => {
  vi.mocked(getStorage).mockResolvedValue({ ...structuredClone(document), default: { ...document.default, separateFolder: true } });
  renderStorage();
  expect((await screen.findByRole("switch", { name: "视频及 GIF 原文件 按媒体类型分文件夹" })).getAttribute("aria-checked")).toBe("true");
});

it("supports text alone and tweet ID alone without allowing an empty filename", async () => {
  vi.mocked(saveStorage).mockImplementation(async submitted => ({ ...submitted, version: submitted.version + 1 }));
  const user = userEvent.setup(); renderStorage();
  await user.click(await screen.findByRole("button", { name: "展开默认规则" }));
  await user.click(await screen.findByRole("combobox", { name: "命名策略" }));
  await user.click(screen.getByRole("option", { name: "自定义组合" }));
  await user.click(screen.getByRole("combobox", { name: "添加命名字段" }));
  await user.click(screen.getByRole("option", { name: "正文" }));
  await user.click(screen.getByRole("button", { name: "媒体编号 移除" }));
  expect(screen.getByRole("button", { name: "正文 移除" }).hasAttribute("disabled")).toBe(true);
  await user.click(screen.getByRole("button", { name: "保存存储设置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(1));
  expect(vi.mocked(saveStorage).mock.calls[0][0].default.filenameTemplate).toBe("{text}.{ext}");
  await user.click(screen.getByRole("combobox", { name: "添加命名字段" }));
  await user.click(screen.getByRole("option", { name: "推文编号" }));
  await user.click(screen.getByRole("button", { name: "正文 移除" }));
  await user.click(screen.getByRole("button", { name: "保存存储设置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(2));
  expect(vi.mocked(saveStorage).mock.calls[1][0].default.filenameTemplate).toBe("{tweet_id}.{ext}");
});

it("summarizes inherited rules and keeps custom rules editable", async () => {
  renderStorage();
  await screen.findByRole("button", { name: "展开默认规则" });
  expect(screen.queryByRole("combobox", { name: "分类存储策略" })).toBeNull();
  expect(globalThis.document.querySelector("details")).toBeNull();
  expect(screen.getByRole("button", { name: "迁移历史资源" })).toBeTruthy();
  const inherited = screen.getByRole("group", { name: "图片原文件" });
  expect(within(inherited).getAllByText("使用默认").length).toBeGreaterThan(0);
  expect(screen.queryByRole("combobox", { name: "图片原文件 命名策略" })).toBeNull();
  expect(screen.getByRole("combobox", { name: "视频及 GIF 原文件 命名策略" })).toBeTruthy();
  expect(screen.getByRole("combobox", { name: "视频及 GIF 原文件 存储目标" }).textContent).toContain("NAS");
});

it("折叠默认规则时仍保留草稿，并在离开前触发保护", async () => {
  const user = userEvent.setup(); renderStorage();
  await screen.findByRole("button", { name: "展开默认规则" });
  expect(screen.queryByRole("combobox", { name: "分类存储策略" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "展开默认规则" }));
  await user.click(screen.getByRole("switch", { name: "按媒体类型分文件夹" }));
  await user.click(screen.getByRole("button", { name: "收起默认规则" }));
  await user.click(screen.getByRole("button", { name: "展开默认规则" }));
  expect(screen.getByRole("switch", { name: "按媒体类型分文件夹" }).getAttribute("aria-checked")).toBe("true");
  await waitFor(() => expect(window.dispatchEvent(new Event("settings:before-navigate", { cancelable: true }))).toBe(false));
  expect(window.dispatchEvent(new Event("beforeunload", { cancelable: true }))).toBe(false);
});

it("copies the default rule when customizing a media type and restores inheritance", async () => {
  vi.mocked(saveStorage).mockImplementation(async submitted => ({ ...submitted, version: submitted.version + 1 }));
  const user = userEvent.setup(); renderStorage();
  const custom = await screen.findByRole("switch", { name: "图片原文件 单独设置" });
  await user.click(custom);
  await user.click(screen.getByRole("combobox", { name: "图片原文件 命名策略" }));
  await user.click(screen.getByRole("option", { name: "用户名 + 推文编号" }));
  await user.click(screen.getByRole("button", { name: "保存存储设置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(1));
  expect(vi.mocked(saveStorage).mock.calls[0][0].rules.image).toEqual({ ...document.default, filenameTemplate: "{username}_{tweet_id}_{index}.{ext}" });
  expect(vi.mocked(saveStorage).mock.calls[0][0].rules.video).toEqual(document.rules.video);
  await waitFor(() => expect(screen.queryByText("尚未保存")).toBeNull());
  await user.click(custom);
  await user.click(screen.getByRole("button", { name: "保存存储设置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(2));
  expect(vi.mocked(saveStorage).mock.calls[1][0].rules.image).toBeUndefined();
  expect(screen.queryByRole("combobox", { name: "图片原文件 命名策略" })).toBeNull();
  expect(within(screen.getByRole("group", { name: "图片原文件" })).getByText(/命名策略: 媒体编号/)).toBeTruthy();
});

it("disables migration Popconfirm when disabled or dirty", async () => {
  const user = userEvent.setup();
  renderStorage();
  const migrateBtn = await screen.findByRole("button", { name: "迁移历史资源" });
  expect(migrateBtn.hasAttribute("disabled")).toBe(true);
  await user.click(migrateBtn);
  expect(screen.queryByText("将历史资源复制到已保存的存储策略？")).toBeNull();
  expect(migrateStorage).not.toHaveBeenCalled();
});

it("does not mark StorageManagement dirty when switching FilenameStrategy to custom without changing template", async () => {
  const user = userEvent.setup();
  renderStorage();
  await user.click(await screen.findByRole("button", { name: "展开默认规则" }));
  expect(screen.queryByText("尚未保存")).toBeNull();
  expect(screen.getByRole("button", { name: "保存存储设置" }).hasAttribute("disabled")).toBe(true);

  await user.click(screen.getByRole("combobox", { name: "命名策略" }));
  await user.click(screen.getByRole("option", { name: "自定义组合" }));

  expect(screen.queryByText("尚未保存")).toBeNull();
  expect(screen.getByRole("button", { name: "保存存储设置" }).hasAttribute("disabled")).toBe(true);
});

it("invalidates local-directories cache when reopening the local directory picker modal", async () => {
  const user = userEvent.setup();
  const { client } = renderStorage();
  const invalidateSpy = vi.spyOn(client, "invalidateQueries");
  const browseBtn = await screen.findByRole("button", { name: "选择目录" });
  await user.click(browseBtn);
  expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["local-directories"] });
});
