// @vitest-environment jsdom
import React from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { renderWithProviders, screen } from "../../test/render";
import { getDerivatives, getStorage, getTweets, saveStorage, type StorageDocument } from "../../lib/archive";
import { api } from "../../lib/api";
import { StorageManagement } from "./StorageManagement";

vi.mock("../../lib/archive", async original => ({ ...await original<typeof import("../../lib/archive")>(), getStorage: vi.fn(), getDerivatives: vi.fn(), getTweets: vi.fn(), saveStorage: vi.fn() }));
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
  return renderWithProviders(<QueryClientProvider client={client}><StorageManagement /></QueryClientProvider>);
}
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(getStorage).mockResolvedValue(structuredClone(document));
  vi.mocked(getDerivatives).mockResolvedValue({ items: [] });
  vi.mocked(getTweets).mockResolvedValue({ items: [], pagination: { page: 1, pageSize: 1, total: 0, totalPages: 1 } });
});

it("preserves edits made during a save and uses the returned policy version on the next save", async () => {
  let resolveSave!: (document: StorageDocument) => void;
  vi.mocked(saveStorage).mockImplementationOnce(() => new Promise(resolve => { resolveSave = resolve; }));
  vi.mocked(saveStorage).mockImplementationOnce(async submitted => ({ ...submitted, version: 3 }));
  const user = userEvent.setup(); renderStorage();
  await user.click(await screen.findByRole("switch", { name: "启用分类存储策略" }));
  await user.click(screen.getByRole("button", { name: "保存配置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(1));
  const filename = screen.getAllByRole("textbox", { name: "命名模板" })[0];
  await user.clear(filename); await user.type(filename, "updated.jpg");
  await act(async () => resolveSave({ ...vi.mocked(saveStorage).mock.calls[0][0], version: 2 }));
  expect((filename as HTMLInputElement).value).toBe("updated.jpg");
  expect(screen.getByText("尚未保存")).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "保存配置" }));
  await waitFor(() => expect(saveStorage).toHaveBeenCalledTimes(2));
  const submitted = vi.mocked(saveStorage).mock.calls[1][0];
  expect(submitted.version).toBe(2); expect(submitted.default.filenameTemplate).toBe("updated.jpg");
  await waitFor(() => expect(screen.queryByText("尚未保存")).toBeNull());
});

it("prevents deleting targets referenced by default and override policies and exposes preview failures", async () => {
  const user = userEvent.setup(); renderStorage();
  await screen.findAllByText("Local");
  const rows = screen.getAllByRole("row");
  const local = rows.find(row => within(row).queryByText("Local"))!;
  const nas = rows.find(row => within(row).queryAllByText("NAS").length > 0)!;
  const spare = rows.find(row => within(row).queryByText("Spare"))!;
  expect(within(local).getByRole("button", { name: "删除" }).hasAttribute("disabled")).toBe(true);
  expect(within(nas).getByRole("button", { name: "删除" }).hasAttribute("disabled")).toBe(true);
  expect(within(spare).getByRole("button", { name: "删除" }).hasAttribute("disabled")).toBe(false);
  vi.mocked(api).mockRejectedValueOnce(new Error("Template escaped root"));
  await user.click(screen.getByRole("button", { name: "预览实际路径 图片原文件" }));
  expect(await screen.findByText("Template escaped root")).toBeTruthy();
  expect(screen.getByRole("button", { name: "保存配置" }).hasAttribute("disabled")).toBe(true);
});
