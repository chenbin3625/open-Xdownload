// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { beforeEach, expect, it, vi } from "vitest";
import {
  checkAllXAccounts,
  checkXAccount,
  deleteXAccount,
  getXAccounts,
  importXAccounts,
  unlockXAccount,
  updateXAccount,
  type XAccountCheckAllResult,
  type XAccountView,
} from "../../lib/api";
import { act, renderWithProviders, screen, waitFor } from "../../test/render";
import { XAccountsSettings } from "./XAccountsSettings";

vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  getXAccounts: vi.fn(),
  importXAccounts: vi.fn(),
  checkXAccount: vi.fn(),
  checkAllXAccounts: vi.fn(),
  updateXAccount: vi.fn(),
  deleteXAccount: vi.fn(),
  unlockXAccount: vi.fn(),
}));

function makeAccount(overrides: Partial<XAccountView> = {}): XAccountView {
  return {
    id: 1,
    screenName: "alice",
    authToken: "abcd…wxyz",
    csrfToken: "csrf…csrf",
    hasExtraCookies: false,
    userAgent: "",
    proxy: "",
    isPrimary: true,
    source: "db",
    active: true,
    errorMsg: "",
    lastUsed: null,
    verifiedAt: null,
    createdAt: "2026-11-01T00:00:00Z",
    locks: [],
    requestsToday: 0,
    requestsByQueue: [],
    ...overrides,
  };
}

function renderAccounts() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithProviders(
    <QueryClientProvider client={client}>
      <XAccountsSettings />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(getXAccounts).mockResolvedValue({
    scraper: "legacy",
    accounts: [makeAccount()],
  });
});

it("renders the scraper badge and account rows", async () => {
  vi.mocked(getXAccounts).mockResolvedValue({
    scraper: "xscrape",
    accounts: [
      makeAccount({ requestsToday: 12 }),
      makeAccount({
        id: 2,
        screenName: "",
        authToken: "wx12…yz34",
        isPrimary: false,
        source: "env",
        active: false,
        errorMsg: "Cookie 已失效",
      }),
    ],
  });
  renderAccounts();

  expect(await screen.findByText("@alice")).toBeTruthy();
  expect(screen.getByText("xscrape")).toBeTruthy();
  expect(screen.getByText("wx12…yz34")).toBeTruthy();
  expect(screen.getByText("环境变量 · 只读")).toBeTruthy();
  expect(screen.getByText("停用")).toBeTruthy();
  expect(screen.getByText("Cookie 已失效")).toBeTruthy();
  // 表头 + alice 的主账号标记
  expect(screen.getAllByText("主账号")).toHaveLength(2);
  expect(screen.getByText("12")).toBeTruthy();
});

it("shows an empty state that opens the import dialog", async () => {
  vi.mocked(getXAccounts).mockResolvedValue({ scraper: "legacy", accounts: [] });
  const user = userEvent.setup();
  renderAccounts();

  expect(
    await screen.findByText("还没有 X 账号。导入第一个账号后即可开始下载。"),
  ).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "导入第一个账号" }));
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByRole("textbox", { name: "账号导入内容" })).toBeTruthy();
});

it("shows created count and import issues after importing and preserves textarea on partial failure", async () => {
  vi.mocked(importXAccounts).mockResolvedValue({
    created: [makeAccount({ id: 3, screenName: "bob" })],
    issues: [
      { index: 1, reason: "缺少 ct0" },
      { index: 2, reason: "无法解析 JSON" },
    ],
  });
  const user = userEvent.setup();
  renderAccounts();
  await screen.findByText("@alice");

  await user.click(screen.getByRole("button", { name: "导入账号" }));
  const importBox = screen.getByRole("textbox", { name: "账号导入内容" }) as HTMLTextAreaElement;
  expect(importBox.getAttribute("spellcheck")).toBe("false");
  expect(importBox.getAttribute("autocomplete")).toBe("off");
  await user.type(importBox, "bad-entry");
  await user.click(screen.getByRole("button", { name: "导入" }));

  expect(await screen.findByText("已导入 1 个账号")).toBeTruthy();
  expect(screen.getByText("@bob")).toBeTruthy();
  expect(screen.getByText("第 2 条")).toBeTruthy();
  expect(screen.getByText("缺少 ct0")).toBeTruthy();
  expect(screen.getByText("第 3 条")).toBeTruthy();
  expect(screen.getByText("无法解析 JSON")).toBeTruthy();
  // Partial failure must preserve pasted input so cookies are not lost
  expect(importBox.value).toBe("bad-entry");
  await waitFor(() => expect(getXAccounts).toHaveBeenCalledTimes(2));
});

it("disables deletion and cookie replacement for env accounts", async () => {
  vi.mocked(getXAccounts).mockResolvedValue({
    scraper: "legacy",
    accounts: [makeAccount({ source: "env" })],
  });
  const user = userEvent.setup();
  renderAccounts();
  await screen.findByText("@alice");

  const deleteButton = screen.getByRole("button", { name: "删除 @alice" });
  expect(deleteButton.hasAttribute("disabled")).toBe(true);
  expect(deleteButton.getAttribute("title")).toBe("环境变量账号为只读，不能删除");

  await user.click(screen.getByRole("button", { name: "编辑 @alice" }));
  expect(
    await screen.findByText("环境变量账号的 Cookie 由环境变量提供，不能在这里替换。"),
  ).toBeTruthy();
  expect(screen.queryByRole("textbox", { name: "替换 Cookie" })).toBeNull();
});

it("asks for confirmation before deleting and refreshes the list", async () => {
  vi.mocked(deleteXAccount).mockResolvedValue(undefined);
  const user = userEvent.setup();
  renderAccounts();
  await screen.findByText("@alice");

  await user.click(screen.getByRole("button", { name: "删除 @alice" }));
  expect(await screen.findByText("删除账号？")).toBeTruthy();
  expect(deleteXAccount).not.toHaveBeenCalled();

  await user.click(screen.getByRole("button", { name: "删除" }));
  await waitFor(() => expect(deleteXAccount).toHaveBeenCalledWith(1, expect.anything()));
  await waitFor(() => expect(getXAccounts).toHaveBeenCalledTimes(2));
});

it("refreshes the list after checking an account", async () => {
  vi.mocked(checkXAccount).mockResolvedValue({
    ok: true,
    screenName: "alice",
    account: makeAccount(),
  });
  const user = userEvent.setup();
  renderAccounts();
  await screen.findByText("@alice");

  await user.click(screen.getByRole("button", { name: "检测 @alice" }));

  await waitFor(() => expect(checkXAccount).toHaveBeenCalledWith(1, expect.anything()));
  await waitFor(() => expect(getXAccounts).toHaveBeenCalledTimes(2));
});

it("shows active locks with remaining time and hides expired ones", async () => {
  vi.mocked(unlockXAccount).mockResolvedValue(makeAccount());
  vi.mocked(getXAccounts).mockResolvedValue({
    scraper: "legacy",
    accounts: [
      makeAccount({
        locks: [
          {
            queue: "UserMedia",
            until: new Date(Date.now() + 90_000).toISOString(),
            reason: "429",
          },
          {
            queue: "*",
            until: new Date(Date.now() - 1_000).toISOString(),
            reason: "已过期",
          },
        ],
      }),
    ],
  });
  const user = userEvent.setup();
  renderAccounts();

  expect(await screen.findByText("UserMedia")).toBeTruthy();
  expect(screen.getByText(/剩余/)).toBeTruthy();
  expect(screen.queryByText("账号级冷却")).toBeNull();

  await user.click(screen.getByRole("button", { name: "解锁 @alice" }));
  await waitFor(() => expect(unlockXAccount).toHaveBeenCalledWith(1, expect.anything()));
});

it("sends a patch when toggling an account", async () => {
  vi.mocked(updateXAccount).mockResolvedValue(makeAccount({ active: false }));
  const user = userEvent.setup();
  renderAccounts();
  await screen.findByText("@alice");

  await user.click(screen.getByRole("switch", { name: "启用状态 @alice" }));
  await waitFor(() =>
    expect(updateXAccount).toHaveBeenCalledWith(1, { active: false }),
  );
  await waitFor(() => expect(getXAccounts).toHaveBeenCalledTimes(2));
});

it("enforces mutex between checkAll and row actions, and disables spellcheck/autocomplete on cookie textareas", async () => {
  let resolveCheckAll!: (value: XAccountCheckAllResult) => void;
  let resolveRowCheck!: (value: { ok: boolean; screenName: string; account: XAccountView }) => void;
  vi.mocked(checkAllXAccounts).mockImplementation(() => new Promise<XAccountCheckAllResult>((resolve) => { resolveCheckAll = resolve; }));
  vi.mocked(checkXAccount).mockImplementation(() => new Promise((resolve) => { resolveRowCheck = resolve; }));

  const user = userEvent.setup();
  renderAccounts();
  await screen.findByText("@alice");

  // Edit cookie textarea disables spellcheck & autocomplete
  await user.click(screen.getByRole("button", { name: "编辑 @alice" }));
  const editCookieBox = screen.getByRole("textbox", { name: "替换 Cookie" });
  expect(editCookieBox.getAttribute("spellcheck")).toBe("false");
  expect(editCookieBox.getAttribute("autocomplete")).toBe("off");
  await user.click(screen.getByRole("button", { name: "取消" }));

  // While checkAll is pending, row edit & delete are disabled
  await user.click(screen.getByRole("button", { name: "全部检测" }));
  await waitFor(() => expect(checkAllXAccounts).toHaveBeenCalledTimes(1));
  expect(screen.getByRole("button", { name: "编辑 @alice" }).hasAttribute("disabled")).toBe(true);
  expect(screen.getByRole("button", { name: "删除 @alice" }).hasAttribute("disabled")).toBe(true);
  expect(screen.getByRole("button", { name: "检测 @alice" }).hasAttribute("disabled")).toBe(true);

  await act(async () => resolveCheckAll({ results: [], accounts: [makeAccount()] }));
  await waitFor(() => expect(screen.getByRole("button", { name: "编辑 @alice" }).hasAttribute("disabled")).toBe(false));

  // While a row action is pending, checkAll is disabled
  await user.click(screen.getByRole("button", { name: "检测 @alice" }));
  await waitFor(() => expect(checkXAccount).toHaveBeenCalledTimes(1));
  expect(screen.getByRole("button", { name: "全部检测" }).hasAttribute("disabled")).toBe(true);

  await act(async () => resolveRowCheck({ ok: true, screenName: "alice", account: makeAccount() }));
  await waitFor(() => expect(screen.getByRole("button", { name: "全部检测" }).hasAttribute("disabled")).toBe(false));
});

it("clears importText after import and editCookie after edit modal closes, and tracks concurrent row updates independently", async () => {
  vi.mocked(importXAccounts).mockResolvedValue({
    created: [makeAccount({ id: 3, screenName: "bob" })],
    issues: [],
  });
  let resolveRow1!: (v: XAccountView) => void;
  let resolveRow2!: (v: XAccountView) => void;
  vi.mocked(getXAccounts).mockResolvedValue({
    scraper: "legacy",
    accounts: [makeAccount({ id: 1, screenName: "alice" }), makeAccount({ id: 2, screenName: "bob", isPrimary: false })],
  });
  vi.mocked(updateXAccount)
    .mockImplementationOnce(() => new Promise(r => { resolveRow1 = r; }))
    .mockImplementationOnce(() => new Promise(r => { resolveRow2 = r; }));

  const user = userEvent.setup();
  renderAccounts();
  await screen.findByText("@alice");

  // Import clears importText on success
  await user.click(screen.getByRole("button", { name: "导入账号" }));
  const importBox = screen.getByRole("textbox", { name: "账号导入内容" }) as HTMLTextAreaElement;
  await user.type(importBox, "auth_token=secret; ct0=token");
  await user.click(screen.getByRole("button", { name: "导入" }));
  await waitFor(() => expect(importBox.value).toBe(""));
  await user.click(screen.getByRole("button", { name: "关闭" }));

  // Edit modal clears editCookie on close
  await user.click(screen.getByRole("button", { name: "编辑 @alice" }));
  const cookieBox = screen.getByRole("textbox", { name: "替换 Cookie" }) as HTMLTextAreaElement;
  await user.type(cookieBox, "auth_token=new; ct0=new");
  await user.click(screen.getByRole("button", { name: "取消" }));
  await user.click(screen.getByRole("button", { name: "编辑 @alice" }));
  expect((screen.getByRole("textbox", { name: "替换 Cookie" }) as HTMLTextAreaElement).value).toBe("");
  await user.click(screen.getByRole("button", { name: "取消" }));

  // Concurrent row switches don't disable or clobber other rows
  const switch1 = screen.getByRole("switch", { name: "启用状态 @alice" });
  const switch2 = screen.getByRole("switch", { name: "启用状态 @bob" });
  await user.click(switch1);
  expect(switch2.hasAttribute("disabled")).toBe(false);
  await user.click(switch2);
  expect(updateXAccount).toHaveBeenCalledTimes(2);
  resolveRow1(makeAccount({ id: 1, screenName: "alice", active: false }));
  resolveRow2(makeAccount({ id: 2, screenName: "bob", isPrimary: false, active: false }));
});
