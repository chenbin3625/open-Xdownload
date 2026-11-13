import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import english from "../src/locales/en.json" with { type: "json" };
import chinese from "../src/locales/zh-CN.json" with { type: "json" };

// 账号池页面的增删改、检测与解锁，跑在真实后端上。
//
// 检测会请求 X 的账号接口：凭据是本测试生成的随机假值，结果只能是"未通过"
// （X 明确拒绝）或"无法判断"（预览机访问不到 X）。两种情况都不能把账号检测成通过。
// 解锁用例需要先有一把锁：设置 OPEN_XDOWNLOAD_E2E_DB 为预览服务的 SQLite 路径时，
// 直接往 x_account_locks 写一把锁；未设置时跳过该用例。

const dbPath = process.env.OPEN_XDOWNLOAD_E2E_DB ?? "";

type AccountView = {
  id: number;
  authToken: string;
  csrfToken: string;
  proxy: string;
  userAgent: string;
  source: "db" | "env";
  active: boolean;
  isPrimary: boolean;
  errorMsg: string;
  locks: { queue: string; until: string; reason: string }[];
};

function randomToken(length: number) {
  const alphabet = "0123456789abcdef";
  return Array.from({ length }, () => alphabet[Math.floor(Math.random() * alphabet.length)]).join("");
}

function masked(token: string) {
  return token.length <= 8 ? "********" : `${token.slice(0, 4)}…${token.slice(-4)}`;
}

async function listAccounts(request: APIRequestContext): Promise<AccountView[]> {
  const response = await request.get("/api/x-accounts");
  expect(response.ok()).toBe(true);
  return (await response.json()).accounts;
}

async function findAccount(request: APIRequestContext, authToken: string) {
  return (await listAccounts(request)).find(account => account.authToken === masked(authToken));
}

async function deleteIfPresent(request: APIRequestContext, authToken: string) {
  const account = await findAccount(request, authToken);
  if (account && account.source === "db") {
    await request.delete(`/api/x-accounts/${account.id}`);
  }
}

async function openAccountsPage(page: Page, en: boolean) {
  await page.addInitScript(language => localStorage.setItem("open-xdownload-language", language), en ? "en" : "zh-CN");
  await page.goto("/settings");
  await expect(page.locator(".x-accounts-table")).toBeVisible();
}

async function lockAccount(id: number, queue: string, minutes: number) {
  const { DatabaseSync } = await import("node:sqlite");
  // 预览服务同时在写这个库（WAL）：给写锁冲突留出等待时间。
  const db = new DatabaseSync(dbPath, { timeout: 5000 });
  try {
    const until = new Date(Date.now() + minutes * 60_000).toISOString().replace(/\.\d+Z$/, "Z");
    db.prepare(
      "INSERT INTO x_account_locks (account_id, queue, until, reason) VALUES (?, ?, ?, ?) " +
        "ON CONFLICT(account_id, queue) DO UPDATE SET until = excluded.until, reason = excluded.reason",
    ).run(id, queue, until, "e2e");
  } finally {
    db.close();
  }
}

test.describe("X account pool", () => {
  test("imports, edits, checks and deletes accounts", async ({ page }, info) => {
    const en = info.project.name.endsWith("-en");
    const text = (key: string) => ((en ? english : chinese) as Record<string, string>)[key] ?? key;
    const authToken = randomToken(40);
    const ct0 = randomToken(32);
    const errors: string[] = [];
    page.on("pageerror", error => errors.push(error.message));
    try {
      await openAccountsPage(page, en);

      // 导入：一条有效 Cookie 加一条缺 ct0 的条目。
      await page.getByRole("button", { name: text("导入账号"), exact: true }).click();
      const dialog = page.getByRole("dialog").filter({ hasText: text("导入 X 账号") });
      await dialog.getByRole("textbox", { name: text("账号导入内容") }).fill(
        `guest_id=v1%3A1; auth_token=${authToken}; ct0=${ct0}\nauth_token=${randomToken(40)}`,
      );
      await dialog.getByRole("button", { name: text("导入"), exact: true }).click();
      await expect(dialog.getByText(text("已导入 {{value0}} 个账号").replace("{{value0}}", "1"))).toBeVisible();
      await expect(dialog.getByText(text("第 {{value0}} 条").replace("{{value0}}", "2"))).toBeVisible();
      await dialog.getByRole("button", { name: text("关闭"), exact: true }).click();

      const label = masked(authToken);
      const row = page.getByRole("row").filter({ hasText: label });
      await expect(row).toBeVisible();
      await expect(row).not.toContainText(authToken);
      const created = await findAccount(page.request, authToken);
      expect(created).toMatchObject({ source: "db", active: true, csrfToken: masked(ct0) });

      // 编辑代理与 UA：密码在列表里打码，再次保存时打码值要还原成原密码。
      await row.getByRole("button", { name: `${text("编辑")} ${label}`, exact: true }).click();
      const edit = page.getByRole("dialog").filter({ hasText: text("编辑账号") });
      await edit.getByRole("textbox", { name: text("代理") }).fill("http://e2e-user:e2e-secret@127.0.0.1:7890");
      await edit.getByRole("textbox", { name: text("User-Agent") }).fill("e2e-agent/1.0");
      await edit.getByRole("button", { name: text("保存"), exact: true }).click();
      await expect(edit).toHaveCount(0);
      const edited = await findAccount(page.request, authToken);
      expect(edited?.userAgent).toBe("e2e-agent/1.0");
      expect(edited?.proxy).toContain("127.0.0.1:7890");
      expect(edited?.proxy).not.toContain("e2e-secret");

      await row.getByRole("button", { name: `${text("编辑")} ${label}`, exact: true }).click();
      await edit.getByRole("textbox", { name: text("User-Agent") }).fill("e2e-agent/2.0");
      await edit.getByRole("button", { name: text("保存"), exact: true }).click();
      await expect(edit).toHaveCount(0);
      const reEdited = await findAccount(page.request, authToken);
      expect(reEdited?.userAgent).toBe("e2e-agent/2.0");
      expect(reEdited?.proxy).toBe(edited?.proxy);

      // 停用后再启用。
      const toggle = row.getByRole("switch", { name: `${text("启用状态")} ${label}`, exact: true });
      await toggle.click();
      await expect.poll(async () => (await findAccount(page.request, authToken))?.active).toBe(false);
      await toggle.click();
      await expect.poll(async () => (await findAccount(page.request, authToken))?.active).toBe(true);

      // 检测：假凭据不能被判定为通过；账号要么被确认失效而停用，要么只记录错误。
      await page.request.patch(`/api/x-accounts/${created!.id}`, { data: { proxy: "" } });
      await page.reload();
      await expect(row).toBeVisible();
      const checkResponse = page.waitForResponse(response =>
        response.url().endsWith(`/api/x-accounts/${created!.id}/check`) && response.request().method() === "POST",
        { timeout: 60_000 },
      );
      await row.getByRole("button", { name: `${text("检测")} ${label}`, exact: true }).click();
      const check = await (await checkResponse).json();
      expect(check.ok).toBe(false);
      await expect(page.locator(".semi-toast").last()).toContainText(text("账号检测未通过"));
      const checked = await findAccount(page.request, authToken);
      expect(checked?.errorMsg).not.toBe("");

      // 删除需要二次确认。
      await row.getByRole("button", { name: `${text("删除")} ${label}`, exact: true }).click();
      const confirm = page.getByRole("alertdialog", { name: text("删除账号？") });
      await confirm.getByRole("button", { name: text("删除"), exact: true }).click();
      await expect(row).toHaveCount(0);
      expect(await findAccount(page.request, authToken)).toBeUndefined();
      expect(errors).toEqual([]);
    } finally {
      await deleteIfPresent(page.request, authToken);
    }
  });

  test("unlocks an account with active locks", async ({ page }, info) => {
    test.skip(!dbPath, "设置 OPEN_XDOWNLOAD_E2E_DB 为预览服务的数据库路径后运行");
    const en = info.project.name.endsWith("-en");
    const text = (key: string) => ((en ? english : chinese) as Record<string, string>)[key] ?? key;
    const authToken = randomToken(40);
    try {
      const created = await page.request.post("/api/x-accounts", {
        data: { cookie: `auth_token=${authToken}; ct0=${randomToken(32)}` },
      });
      expect(created.status()).toBe(201);
      const account: AccountView = await created.json();
      await lockAccount(account.id, "*", 30);
      await lockAccount(account.id, "UserMedia", 30);

      await openAccountsPage(page, en);
      const label = masked(authToken);
      const row = page.getByRole("row").filter({ hasText: label });
      await expect(row.getByText(text("账号级冷却"), { exact: true })).toBeVisible();
      await expect(row.getByText("UserMedia", { exact: true })).toBeVisible();

      await row.getByRole("button", { name: `${text("解锁")} ${label}`, exact: true }).click();
      await expect(page.locator(".semi-toast").last()).toContainText(text("账号已解锁"));
      await expect(row.getByText(text("账号级冷却"), { exact: true })).toHaveCount(0);
      expect((await findAccount(page.request, authToken))?.locks).toEqual([]);
    } finally {
      await deleteIfPresent(page.request, authToken);
    }
  });
});
