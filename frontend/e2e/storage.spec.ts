import { expect, test } from "@playwright/test";

test("storage presets, custom validation and previews survive saving", async ({ page, request }, testInfo) => {
  const initial = await (await request.get("/api/settings")).json();
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    await page.goto("/settings");
    await page.getByRole("button", { name: "恢复默认路径" }).click();
    const editors = page.locator(".storage-path-editor");
    const image = editors.nth(0);
    const video = editors.nth(1);
    const thumbnail = editors.nth(2);
    const save = page.getByRole("button", { name: "保存更改", exact: true });
    await expect(image.locator(".storage-path-preview")).toContainText("/images/alice/1234567890_1.jpg");
    await expect(page.getByLabel("图片路径模板", { exact: true })).toHaveCount(0);
    await page.getByLabel("图片根目录", { exact: true }).fill("photos/originals");
    await expect(image.locator(".storage-path-preview")).toContainText("/photos/originals/alice/1234567890_1.jpg");

    await image.locator(".semi-select").click();
    await page.locator(".semi-select-option").filter({ hasText: "按作者、推文分文件夹" }).click();
    await expect(image.locator(".storage-path-preview")).toContainText("/alice/1234567890/1234567890_1.jpg");
    await video.locator(".semi-select").click();
    await page.locator(".semi-select-option").filter({ hasText: "全部放在同一文件夹" }).click();
    await expect(video.locator(".storage-path-preview")).toContainText("/videos/1234567890_1.mp4");
    await thumbnail.locator(".semi-select").click();
    await page.locator(".semi-select-option").filter({ hasText: "自定义路径模板" }).click();
    const template = thumbnail.locator(".storage-custom-template input");
    await template.fill("{author}/{tweet_id}.{extension}");
    await expect(thumbnail.getByRole("alert")).toContainText("media_id");
    await expect(save).toBeDisabled();
    await template.fill("{data}/covers/{author}/{media_id}.{extension}");
    await expect(thumbnail.locator(".storage-path-preview")).toContainText("/covers/alice/");
    await expect(thumbnail.locator(".storage-path-warning")).toBeVisible();

    await page.getByLabel("图片根目录", { exact: true }).fill("../escape");
    await expect(image.getByRole("alert")).toContainText("不能包含");
    await expect(save).toBeDisabled();
    await page.getByLabel("图片根目录", { exact: true }).fill("photos/originals");
    await expect(save).toBeEnabled();
    await save.click();
    await expect(save).toBeDisabled();
    await page.reload();
    await expect(page.getByLabel("图片根目录", { exact: true })).toHaveValue("photos/originals");
    await expect(image.locator(".semi-select")).toContainText("按作者、推文分文件夹");
    await expect(template).toHaveValue("{data}/covers/{author}/{media_id}.{extension}");
    await expect(thumbnail.locator(".storage-path-preview")).toContainText("/covers/alice/");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await page.locator(".storage-location").scrollIntoViewIfNeeded();
    await page.screenshot({ path: `test-results/${testInfo.project.name}-storage.png`, fullPage: true });
    await page.getByRole("button", { name: "恢复默认路径" }).click();
    await expect(page.getByLabel("图片根目录", { exact: true })).toHaveValue("images");
    await expect(thumbnail.locator(".storage-custom-template")).toHaveCount(0);
    await expect(thumbnail.locator(".semi-select")).toContainText("按作者分文件夹（默认）");
    expect(errors).toEqual([]);
  } finally {
    expect((await request.put("/api/settings", { data: initial.settings })).ok()).toBe(true);
  }
});

test("failed storage previews can be retried before saving", async ({ page }) => {
  await page.route("**/api/settings/storage-preview", (route) => route.fulfill({
    status: 503,
    contentType: "application/json",
    body: JSON.stringify({ error: "预览暂时不可用" }),
  }));
  await page.goto("/settings");
  await expect(page.getByRole("alert")).toContainText("预览暂时不可用");
  await page.getByLabel("图片根目录", { exact: true }).fill("retry-preview");
  await expect(page.getByRole("button", { name: "保存更改", exact: true })).toBeDisabled();
  await page.unroute("**/api/settings/storage-preview");
  await page.getByRole("button", { name: "重新预览存储路径" }).click();
  await expect(page.locator(".storage-path-preview").first()).toContainText("/retry-preview/alice/1234567890_1.jpg");
  await expect(page.getByRole("button", { name: "保存更改", exact: true })).toBeEnabled();
});
