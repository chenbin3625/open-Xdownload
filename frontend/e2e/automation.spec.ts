import { expect, test } from "@playwright/test";

test.beforeEach(async ({ request }) => {
  await request.post("/__fixture/reset");
  await request.post("/api/cookies", { data: { cookie: "auth_token=FIXTURE_AUTH; ct0=FIXTURE_CSRF" } });
  await expect.poll(async () => (await (await request.get("/api/cookies")).json()).status).toBe("valid");
});

test("batch list and following archives accept targets and create member jobs", async ({ page, request }, info) => {
  await page.goto("/archives");
  await expect(page.getByRole("heading", { name: "归档任务", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "立即归档", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "保存为计划", exact: true })).toBeDisabled();
  await page.getByLabel("归档类型", { exact: true }).click();
  await page.getByText("列表归档", { exact: true }).click();
  await page.getByLabel("归档目标 · 每行一个").fill("1234\nhttps://x.com/i/lists/1234");
  await page.getByRole("button", { name: "立即归档", exact: true }).click();
  await expect(page.getByText("已加入 1 个任务，跳过 1 个重复目标")).toBeVisible();
  await expect.poll(async () => (await (await request.get("/api/users")).json()).length).toBe(2);
  await page.getByLabel("归档类型", { exact: true }).click();
  await page.getByText("关注者归档 · Following", { exact: true }).click();
  await page.getByLabel("归档目标 · 每行一个").fill("@archive_test");
  await page.getByRole("button", { name: "立即归档", exact: true }).click();
  await expect(page.getByText("已加入 1 个任务，跳过 0 个重复目标")).toBeVisible();
  await expect(page.getByLabel("归档目标 · 每行一个")).toHaveValue("");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `test-results/${info.project.name}-batch-archive.png`, fullPage: true });
});

test("schedules can be created edited toggled run and deleted", async ({ page }, info) => {
  await page.goto("/schedules");
  await expect(page).toHaveURL(/\/archives$/);
  if (info.project.name === "mobile") {
    await page.getByRole("button", { name: "打开导航" }).click();
  }
  await expect(page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "归档任务", exact: true })).toHaveCount(1);
  if (info.project.name === "mobile") {
    await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "归档任务", exact: true }).click();
  }
  await page.getByRole("button", { name: "新建计划", exact: true }).click();
  await page.getByLabel("计划名称", { exact: true }).fill("每日列表归档");
  await page.getByRole("dialog").getByLabel("归档目标 · 每行一个").fill("@archive_test");
  await page.getByRole("button", { name: "保存计划", exact: true }).click();
  const table = page.getByRole("table", { name: "计划任务", exact: true });
  await expect(table).toContainText("每日列表归档");
  await page.getByLabel("启用 每日列表归档", { exact: true }).click();
  await expect(table).toContainText("已禁用");
  await page.getByRole("button", { name: "立即运行 每日列表归档", exact: true }).click();
  await expect(table).not.toContainText("尚未运行");
  await page.getByRole("button", { name: "编辑 每日列表归档", exact: true }).click();
  await page.getByLabel("计划名称", { exact: true }).fill("更新后的计划");
  await page.getByRole("button", { name: "保存计划", exact: true }).click();
  await expect(table).toContainText("更新后的计划");
  await expect(page.getByRole("dialog")).toBeHidden();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `test-results/${info.project.name}-schedules.png`, fullPage: true });
  await page.getByRole("button", { name: "删除 更新后的计划", exact: true }).click();
  await page.getByRole("button", { name: "删除", exact: true }).click();
  await expect(page.getByText("暂无计划任务", { exact: true })).toBeVisible();
});

test("quick archive targets carry into schedules without changing the quick form", async ({ page, request }, info) => {
  const taskCount = (await (await request.get("/api/tasks")).json()).total;
  await page.goto("/archives");
  const quick = page.getByRole("region", { name: "快速归档", exact: true });
  const targets = "list 1234\nfollowing @archive_test";
  await quick.getByLabel("归档类型", { exact: true }).click();
  await page.getByText("混合目标", { exact: true }).click();
  await quick.getByLabel("归档目标 · 每行一个").fill(targets);
  await page.getByRole("button", { name: "保存为计划", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("归档目标 · 每行一个")).toHaveValue(targets);
  await expect(dialog.getByLabel("归档类型", { exact: true })).toContainText("混合目标");
  await expect(page.getByRole("button", { name: "保存计划", exact: true })).toBeDisabled();
  await dialog.getByLabel("归档目标 · 每行一个").fill("@cancelled_draft");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(quick.getByLabel("归档目标 · 每行一个")).toHaveValue(targets);
  await page.getByRole("button", { name: "保存为计划", exact: true }).click();
  await expect(dialog.getByLabel("归档目标 · 每行一个")).toHaveValue(targets);
  await dialog.getByLabel("计划名称", { exact: true }).fill("混合归档计划");
  await dialog.getByLabel("计划运行间隔", { exact: true }).fill("120");
  await dialog.getByRole("button", { name: "保存计划", exact: true }).click();
  const table = page.getByRole("table", { name: "计划任务", exact: true });
  await expect(table).toContainText("混合归档计划");
  await expect(table).toContainText("每 120 分钟");
  await expect(table).toContainText("2 个目标");
  expect((await (await request.get("/api/schedules")).json())[0].items).toEqual([
    { kind: "list", target: "1234" },
    { kind: "following", target: "archive_test" },
  ]);
  expect((await (await request.get("/api/tasks")).json()).total).toBe(taskCount);
  await expect(quick.getByLabel("归档目标 · 每行一个")).toHaveValue(targets);
  await page.getByRole("button", { name: "编辑 混合归档计划", exact: true }).click();
  await expect(dialog.getByLabel("归档类型", { exact: true })).toContainText("混合目标");
  await expect(dialog.getByLabel("归档目标 · 每行一个")).toHaveValue("list 1234\nfollowing archive_test");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `test-results/${info.project.name}-schedule-from-targets.png`, fullPage: true });
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await quick.getByLabel("归档目标 · 每行一个").fill(Array(201).fill("@archive_test").join("\n"));
  await expect(page.getByRole("button", { name: "立即归档", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "保存为计划", exact: true })).toBeDisabled();
});

test("cookie pool preserves private credentials and proxy settings persist", async ({ page, request }, info) => {
  await page.goto("/settings");
  await page.getByRole("button", { name: "添加备用 Cookie", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("备用账号");
  await page.locator("#pool-auth").fill("PRIVATE_POOL_AUTH");
  await page.locator("#pool-csrf").fill("PRIVATE_POOL_CSRF");
  await page.getByRole("button", { name: "保存 Cookie", exact: true }).click();
  await expect(page.locator(".pool-list")).toContainText("备用账号");
  expect(await (await request.get("/api/cookie-pool")).text()).not.toContain("PRIVATE_POOL_AUTH");
  await page.getByRole("button", { name: "编辑 Cookie 备用账号", exact: true }).click();
  await expect(page.locator("#pool-auth")).toHaveValue("");
  await page.getByLabel("名称", { exact: true }).fill("更新备用账号");
  await page.getByRole("button", { name: "保存 Cookie", exact: true }).click();
  await expect(page.locator(".pool-list")).toContainText("更新备用账号");
  await page.getByLabel("全局代理地址", { exact: true }).fill("http://127.0.0.1:7890");
  await page.getByRole("button", { name: "保存更改", exact: true }).click();
  await expect.poll(async () => (await (await request.get("/api/settings")).json()).settings.proxy_url).toBe("http://127.0.0.1:7890");
  await page.reload();
  await expect(page.getByLabel("全局代理地址", { exact: true })).toHaveValue("http://127.0.0.1:7890");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `test-results/${info.project.name}-cookie-pool.png`, fullPage: true });
  await page.getByLabel("全局代理地址", { exact: true }).fill("");
  await page.getByRole("button", { name: "保存更改", exact: true }).click();
  await expect.poll(async () => (await (await request.get("/api/settings")).json()).settings.proxy_url).toBe("");
});

test("failed tweets appear in the drawer and manual retry removes recovered entries", async ({ page, request }, info) => {
  await request.post("/__fixture/phase", { data: { phase: 4 } });
  const imported = await (await request.post("/api/tweets", { data: { target: "https://x.com/archive_test/status/1234567890123456790" } })).json();
  await expect.poll(async () => (await (await request.get("/api/failed-tweets")).json()).total).toBe(1);
  await page.goto("/tasks");
  await page.getByRole("button", { name: /失败队列/ }).click();
  await expect(page.locator(".failure-row")).toHaveCount(1);
  await expect(page.locator(".failure-row")).toContainText(/Cookie.*(无效|冷却)/);
  await page.screenshot({ path: `test-results/${info.project.name}-failed-queue.png`, fullPage: true });
  await request.post("/__fixture/phase", { data: { phase: 1 } });
  await request.post("/api/cookies", { data: { cookie: "auth_token=UPDATED_FIXTURE_AUTH; ct0=UPDATED_FIXTURE_CSRF" } });
  await page.getByRole("button", { name: "立即重试失败项", exact: true }).click();
  await expect.poll(async () => (await (await request.get("/api/failed-tweets")).json()).total, { timeout: 45000 }).toBe(0);
  await expect(page.getByText("暂无失败推文", { exact: true })).toBeVisible();
  expect(imported.id).toBeTruthy();
});

test("tweet schedules can run and be edited after targets are normalized", async ({ page, request }) => {
  const created = await request.post("/api/schedules", { data: {
    name: "推文计划", enabled: true, interval_minutes: 60,
    items: [{ kind: "tweet", target: "https://x.com/archive_test/status/1234567890123456781" }],
  } });
  expect(created.ok()).toBe(true);
  await page.goto("/archives");
  const table = page.getByRole("table", { name: "计划任务", exact: true });
  await page.getByRole("button", { name: "立即运行 推文计划", exact: true }).click();
  await expect(table).not.toContainText("尚未运行");
  await page.getByRole("button", { name: "编辑 推文计划", exact: true }).click();
  await page.getByLabel("计划名称", { exact: true }).fill("更新推文计划");
  await page.getByRole("button", { name: "保存计划", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeHidden();
  await expect(table).toContainText("更新推文计划");
});

test("settings retain edits made while a previous save is pending", async ({ page, request }) => {
  await page.goto("/settings");
  const imagePath = page.getByLabel("图片保存路径", { exact: true });
  await expect(imagePath).toBeVisible();
  let releaseResponse!: () => void;
  let notifySent!: () => void;
  const responseGate = new Promise<void>((resolve) => { releaseResponse = resolve; });
  const sent = new Promise<void>((resolve) => { notifySent = resolve; });
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    const response = await route.fetch();
    notifySent();
    await responseGate;
    await route.fulfill({ response });
  });
  const save = page.getByRole("button", { name: "保存更改", exact: true });
  try {
    await imagePath.fill("saved-images");
    await save.click();
    await sent;
    await imagePath.fill("newer-images");
  } finally {
    releaseResponse();
  }
  await expect.poll(async () => (await (await request.get("/api/settings")).json()).settings.image_path).toBe("saved-images");
  await expect(save).not.toHaveClass(/semi-button-loading/);
  await expect(imagePath).toHaveValue("newer-images");
  await expect(save).toBeEnabled();
  await save.click();
  await expect(save).toBeDisabled();
  await expect.poll(async () => (await (await request.get("/api/settings")).json()).settings.image_path).toBe("newer-images");
  await page.reload();
  await expect(imagePath).toHaveValue("newer-images");
});

test("snapshot polling resets pagination and comparison when the selected snapshot changes", async ({ page, request }) => {
  const user = await (await request.post("/api/users", { data: { target: "archive_test" } })).json();
  const snapshot = (id: string, count: number, day: string, status = "complete") => ({
    id, user_id: user.id, direction: "following", status, expected_count: count,
    fetched_count: count, error: status === "partial" ? "Partial fixture" : null,
    created_at: `2026-11-${day}T12:00:00Z`,
  });
  const a = snapshot("a", 51, "02");
  const b = snapshot("b", 1, "01");
  const c = snapshot("c", 1, "03");
  const d = snapshot("d", 1, "04", "partial");
  let snapshots = [a, b];
  const diffRequests: string[] = [];
  await page.route(`**/api/users/${user.id}/snapshots?direction=following`, (route) => route.fulfill({ json: snapshots }));
  await page.route("**/api/snapshots/**", (route) => {
    const url = new URL(route.request().url());
    const id = url.pathname.split("/")[3];
    const selected = [a, b, c, d].find((s) => s.id === id)!;
    const member = { id, username: `member_${id}`, name: `Member ${id}` };
    if (url.pathname.endsWith("/diff")) {
      diffRequests.push(id);
      return route.fulfill({ json: { added: [member], removed: [] } });
    }
    const number = Number(url.searchParams.get("page"));
    return route.fulfill({ json: {
      snapshot: selected, items: number === 1 || id === "a" ? [member] : [],
      total: selected.fetched_count, page: number, limit: 50,
    } });
  });
  await page.goto(`/relationships?user=${user.id}`);
  await expect(page.locator(".member-row")).toContainText("Member a");
  await page.locator(".semi-page-item").filter({ hasText: /^2$/ }).click();
  await expect(page.locator(".semi-page-item-active")).toHaveText("2");
  snapshots = [c, a, b];
  await expect(page.locator(".member-row")).toContainText("Member c");
  await expect(page.locator(".pagination")).toHaveCount(0);
  await page.getByLabel("对比基准快照").click();
  await page.locator(".semi-select-option").filter({ hasText: "51 条" }).click();
  await page.getByRole("button", { name: "查看差异", exact: true }).click();
  await expect(page.getByRole("heading", { name: "新增 (1)", exact: true })).toBeVisible();
  snapshots = [d, c, a, b];
  await expect(page.getByText("Partial fixture", { exact: true })).toBeVisible();
  await expect(page.locator(".member-row")).toContainText("Member d");
  await expect(page.getByRole("button", { name: "查看全部", exact: true })).toHaveCount(0);
  expect(diffRequests).toEqual(["c"]);
});
