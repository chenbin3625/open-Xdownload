import { expect, test } from "@playwright/test";

test("navigation stays accessible and closes after selecting a page", async ({ page }, info) => {
  await page.goto("/");
  const menu = page.getByRole("button", { name: "打开导航" });
  if (info.project.name === "mobile") {
    await expect(menu).toHaveAttribute("aria-expanded", "false");
    await menu.click();
    await expect(menu).toHaveAttribute("aria-expanded", "true");
  } else {
    await expect(menu).toBeHidden();
  }
  const navigation = page.getByRole("navigation", { name: "主导航" });
  await navigation.getByRole("link", { name: "媒体库", exact: true }).click();
  await expect(page.getByRole("heading", { name: "媒体库", exact: true })).toBeVisible();
  if (info.project.name === "mobile") {
    await expect(menu).toHaveAttribute("aria-expanded", "false");
    await expect(navigation).toBeHidden();
    await menu.click();
    await page.keyboard.press("Escape");
    await expect(menu).toHaveAttribute("aria-expanded", "false");
    await menu.click();
    await page.setViewportSize({ width: 1024, height: 900 });
    await expect(page.locator(".navigation-drawer")).toBeHidden();
    await expect(navigation).toHaveCount(1);
  } else {
    await expect(navigation.getByRole("link", { name: "媒体库", exact: true })).toHaveAttribute("aria-current", "page");
  }
});

test("media filters retain their values when collapsed and can be cleared", async ({ page }, info) => {
  await page.goto("/media?tag=example");
  const toggle = page.getByRole("button", { name: "媒体筛选" });
  const dateInput = page.getByLabel("开始日期", { exact: true });
  if (info.project.name === "mobile") {
    await expect(dateInput).toBeHidden();
    await expect(toggle).toContainText("(1)");
    await toggle.click();
  } else {
    await expect(toggle).toBeHidden();
  }
  await expect(dateInput).toBeVisible();
  await dateInput.fill("2025-01-01");
  await expect(page).toHaveURL(/from=2025-01-01/);
  if (info.project.name === "mobile") {
    await toggle.click();
    await expect(dateInput).toBeHidden();
    await toggle.click();
    await expect(dateInput).toHaveValue("2025-01-01");
  }
  await page.locator(".media-toolbar").getByRole("button", { name: "清除筛选", exact: true }).click();
  await expect(page).toHaveURL(/\/media$/);
  await expect(dateInput).toHaveValue("");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: `test-results/${info.project.name}-media-filters.png`, fullPage: true });
});

test("long task errors expand without overflowing the task list", async ({ page }, info) => {
  const error = "Unable to download the requested media. ".repeat(12) + "END_OF_ERROR";
  await page.route("**/api/tasks?*", (route) => route.fulfill({ json: {
    items: [{
      id: "failed-layout", kind: "download", target: "media", label: "媒体下载失败",
      status: "failed", progress: 0, attempts: 3, max_attempts: 3,
      error, result: null, result_size: 0, parent_id: null,
      download_total: 1, download_completed: 0, download_failed: 1,
      download_cancelled: 0, download_size: 0, download_error: null,
      created_at: "2026-11-10T12:00:00Z", updated_at: "2026-11-10T12:00:00Z",
      available_at: "2026-11-10T12:00:00Z",
    }], total: 1, page: 1, limit: 30,
  } }));
  await page.goto("/tasks");
  const details = page.locator(".task-error-details");
  await expect(details.locator("p")).toBeHidden();
  await details.locator("summary").click();
  await expect(details.locator("p")).toHaveText(error);
  await expect(details.locator("p")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await details.locator("summary").click();
  await expect(details.locator("p")).toBeHidden();
  await page.screenshot({ path: `test-results/${info.project.name}-task-error.png`, fullPage: true });
});

test("workspace fits desktop tablet and narrow phones with working media", async ({ page, request }, info) => {
  test.skip(info.project.name !== "desktop", "Viewports are exercised explicitly.");
  await request.post("/__fixture/reset");
  await request.post("/api/cookies", { data: { cookie: "auth_token=FIXTURE_AUTH; ct0=FIXTURE_CSRF" } });
  await request.post("/api/tweets", { data: { target: "https://x.com/archive_test/status/1234567890123456781" } });
  await expect.poll(async () => {
    const media = await (await request.get("/api/media")).json();
    return media.items.filter((item: { has_file: boolean }) => item.has_file).length;
  }, { timeout: 45000 }).toBe(3);
  for (const width of [1440, 1024, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    for (const path of ["/", "/media", "/tasks", "/settings"]) {
      await page.goto(path);
      await expect(page.locator("h1")).toBeVisible();
      await expect(page.locator(".loading")).toHaveCount(0);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${path} at ${width}px`).toBe(true);
      if (path === "/media" || path === "/") {
        await expect(page.locator(".media-preview img").first()).toBeVisible();
        await expect.poll(() => page.locator(".media-preview img").first().evaluate((image: HTMLImageElement) => image.naturalWidth)).toBeGreaterThan(0);
        await page.screenshot({ path: `test-results/workspace-${width}-${path === "/" ? "home" : "media"}.png`, fullPage: true });
      }
    }
  }
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/media");
  await page.getByRole("button", { name: "媒体详情", exact: true }).first().click();
  const modal = page.locator(".media-modal .semi-modal");
  await expect(modal).toBeVisible();
  const bounds = await modal.boundingBox();
  expect(bounds!.width).toBe(390);
  expect(bounds!.height).toBe(844);
  expect(Math.abs(bounds!.x)).toBeLessThanOrEqual(2);
  expect(Math.abs(bounds!.y)).toBeLessThanOrEqual(2);
  await expect(page.locator(".detail-preview img")).toBeVisible();
  await page.screenshot({ path: "test-results/workspace-mobile-fullscreen-media.png" });
});
