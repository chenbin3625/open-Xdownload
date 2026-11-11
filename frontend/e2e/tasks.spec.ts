import { expect, test } from "@playwright/test";

test("downloads appear once per import with compact rows and readable sizes", async ({ page, request }, testInfo) => {
  await request.post("/__fixture/reset");
  expect((await request.post("/api/cookies", {
    data: { cookie: "auth_token=FIXTURE_AUTH; ct0=FIXTURE_CSRF" },
  })).ok()).toBe(true);
  const imported = await request.post("/api/tweets", {
    data: { target: "https://x.com/archive_test/status/1234567890123456781" },
  });
  expect(imported.ok()).toBe(true);
  const task = await imported.json();
  await expect.poll(async () => {
    const data = await (await request.get("/api/tasks?grouped=true")).json();
    return data.items.find((item: { id: string }) => item.id === task.id)?.status;
  }, { timeout: 45000 }).toBe("succeeded");

  const grouped = await (await request.get("/api/tasks?grouped=true")).json();
  const raw = await (await request.get("/api/tasks")).json();
  const job = grouped.items.find((item: { id: string }) => item.id === task.id);
  expect(job.download_total).toBe(3);
  expect(job.download_completed).toBe(3);
  expect(job.download_size).toBeGreaterThan(0);
  expect(raw.total - grouped.total).toBe(3);
  await page.goto("/tasks");
  await expect(page.locator(".task-row")).toHaveCount(grouped.total);
  const row = page.locator(".task-row").filter({ hasText: task.label });
  await expect(row).toContainText("3 / 3 完成");
  await expect(row.locator(".task-download-cell")).toContainText(/\d[\d.]* [KMG]B/);
  await expect(page.locator(".task-table")).not.toContainText("自动下载媒体");
  await expect(page.locator(".task-table")).not.toContainText("字节");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  const bounds = await row.boundingBox();
  expect(bounds!.height).toBeLessThan(testInfo.project.name === "mobile" ? 150 : 90);
  await page.screenshot({ path: `test-results/${testInfo.project.name}-tasks.png`, fullPage: true });
});

test("historical byte results and KB, MB, GB sizes use readable units", async ({ page }, testInfo) => {
  await page.route("**/api/tasks?*", async (route) => {
    await route.fulfill({ json: {
      items: [2048, 8213655, 2 * 1024 ** 3].map((size, index) => ({
        id: `size-${index}`, kind: "download", target: `media-${index}`,
        label: `下载任务 ${index + 1}`, status: "succeeded", progress: 100,
        attempts: 1, max_attempts: 3, error: null,
        result: index === 1 ? "已下载并生成预览，8213655 字节" : "已下载并生成预览",
        result_size: size, parent_id: null,
        download_total: 0, download_completed: 0, download_failed: 0,
        download_cancelled: 0, download_size: size, download_error: null,
        created_at: "2026-11-10T12:00:00Z", updated_at: "2026-11-10T12:00:00Z",
        available_at: "2026-11-10T12:00:00Z",
      })), total: 3, page: 1, limit: 30,
    } });
  });
  await page.goto("/tasks");
  const rows = page.locator(".task-row");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(0).locator(".task-download-cell")).toHaveText("2 KB");
  await expect(rows.nth(1).locator(".task-download-cell")).toHaveText("7.8 MB");
  await expect(rows.nth(2).locator(".task-download-cell")).toHaveText("2 GB");
  await expect(rows.nth(1).locator(".task-result-cell")).toContainText("已下载并生成预览，7.8 MB");
  await expect(page.locator(".task-table")).not.toContainText(/字节|8213655/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `test-results/${testInfo.project.name}-task-sizes.png`, fullPage: true });
});
