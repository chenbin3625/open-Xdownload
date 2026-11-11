import { expect, test } from "@playwright/test";
import type { APIRequestContext } from "@playwright/test";

async function idle(request: APIRequestContext) {
  await expect
    .poll(
      async () => {
        const page = await (await request.get("/api/tasks?limit=100")).json();
        const media = await (await request.get("/api/media?limit=100")).json();
        return (
          page.items.filter((t: { status: string }) =>
            ["queued", "running", "cancelling"].includes(t.status),
          ).length +
          media.items.filter((m: { status: string }) =>
            ["pending", "downloading"].includes(m.status),
          ).length
        );
      },
      { timeout: 45000 },
    )
    .toBe(0);
  const tasks = await (await request.get("/api/tasks?limit=100")).json();
  expect(
    tasks.items.filter((t: { status: string }) => t.status === "failed"),
  ).toEqual([]);
}

test("all pages load from the real API without overflow or script errors", async ({
  page,
  request,
}, testInfo) => {
  await request.post("/__fixture/reset");
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  for (const [path, title] of [
    ["/", "归档概览"],
    ["/users", "用户归档"],
    ["/archives", "归档任务"],
    ["/relationships", "关注关系"],
    ["/tweets", "推文归档"],
    ["/media", "媒体库"],
    ["/tasks", "下载任务"],
    ["/schedules", "归档任务"],
    ["/settings", "设置"],
  ]) {
    await page.goto(path);
    await expect(
      page.getByRole("heading", { name: title, exact: true }),
    ).toBeVisible();
    await expect(page.locator(".loading")).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  }
  await page.screenshot({
    path: `test-results/${testInfo.project.name}-settings.png`,
    fullPage: true,
  });
  expect(errors).toEqual([]);
});

test("cookie form validates input, clears saved credentials and supports updates and deletion", async ({
  page,
  request,
}, testInfo) => {
  await request.post("/__fixture/reset");
  expect((await request.delete("/api/cookies")).ok()).toBe(true);
  await page.goto("/settings");
  const section = page.locator(".settings-section").filter({
    has: page.getByRole("heading", { name: "X 登录 Cookie", exact: true }),
  });
  const authToken = page.getByLabel("auth_token", { exact: true });
  const ct0 = page.getByLabel("ct0", { exact: true });
  const guide = section.locator("details").filter({
    has: page.locator("summary", { hasText: "如何获取 Cookie" }),
  });
  const save = page.getByRole("button", { name: "保存并检测", exact: true });
  const check = section.getByRole("button", { name: "检测", exact: true });
  const remove = page.getByRole("button", { name: "删除 Cookie", exact: true });
  const cookieResponse = () =>
    page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === "/api/cookies" &&
        response.request().method() === "POST",
    );

  await expect(authToken).toHaveAttribute("id", "x-auth-token");
  await expect(ct0).toHaveAttribute("id", "x-ct0");
  await expect(authToken).toHaveAttribute("type", "password");
  await expect(ct0).toHaveAttribute("type", "password");
  await expect(page.locator('input[type="file"]')).toHaveCount(0);
  await expect(section).toContainText("未设置");
  await expect(authToken).toHaveValue("");
  await expect(ct0).toHaveValue("");
  await expect(save).toBeDisabled();
  await expect(check).toBeDisabled();
  await expect(remove).toBeDisabled();
  await expect(guide).toHaveAttribute("open", "");
  await expect(guide.locator("ol")).toBeVisible();
  await expect(guide).toContainText("Application");
  await expect(guide).toContainText("Storage");
  await expect(guide).toContainText("Cookies");
  await expect(guide).toContainText("https://x.com");
  await expect(guide).toContainText("auth_token");
  await expect(guide).toContainText("ct0");
  await expect(guide).toContainText("Value");
  await guide.locator("summary").click();
  await expect(guide.locator("ol")).toBeHidden();
  await guide.locator("summary").click();
  await expect(guide.locator("ol")).toBeVisible();
  await authToken.fill("   ");
  await ct0.fill("FORM_CSRF_SECRET");
  await expect(save).toBeDisabled();
  await authToken.fill("FORM_AUTH_SECRET");
  await ct0.fill("");
  await expect(save).toBeDisabled();
  await ct0.fill("   ");
  await expect(save).toBeDisabled();

  const invalid = "FORM_AUTH_SECRET INVALID";
  await authToken.fill(invalid);
  await ct0.fill("FORM_CSRF_SECRET");
  const invalidResponse = cookieResponse();
  await save.click();
  const rejected = await invalidResponse;
  expect(rejected.status()).toBe(400);
  expect(await rejected.text()).not.toContain("FORM_AUTH_SECRET");
  await expect(page.locator(".semi-toast")).toContainText(/Cookie.*格式/);
  await expect(authToken).toHaveValue(invalid);
  await expect(ct0).toHaveValue("FORM_CSRF_SECRET");
  await expect(save).toBeEnabled();
  expect((await (await request.get("/api/cookies")).json()).status).toBe(
    "missing",
  );
  expect((await (await request.get("/api/tasks")).json()).items).toEqual([]);

  await authToken.fill("  FORM_AUTH_SECRET  ");
  await ct0.fill("  FORM_CSRF_SECRET  ");
  const validResponse = cookieResponse();
  await save.click();
  const saved = await validResponse;
  expect(saved.ok()).toBe(true);
  expect(saved.request().postDataJSON()).toEqual({
    cookie: "auth_token=FORM_AUTH_SECRET; ct0=FORM_CSRF_SECRET",
  });
  expect(await saved.text()).not.toMatch(/FORM_(AUTH|CSRF)_SECRET/);
  await expect(authToken).toHaveValue("");
  await expect(ct0).toHaveValue("");
  await expect(save).toBeDisabled();
  await expect(check).toBeEnabled();
  await expect(remove).toBeEnabled();
  await idle(request);
  await page.reload();
  await expect(authToken).toHaveValue("");
  await expect(ct0).toHaveValue("");
  await expect(section).toContainText("有效");
  await expect(page.locator("body")).not.toContainText(/FORM_(AUTH|CSRF)_SECRET/);
  expect(await (await request.get("/api/settings")).text()).not.toMatch(
    /FORM_(AUTH|CSRF)_SECRET/,
  );

  await authToken.fill("UPDATED_AUTH_SECRET");
  await ct0.fill("UPDATED_CSRF_SECRET");
  await expect(save).toBeEnabled();
  const updateResponse = cookieResponse();
  await save.click();
  const updatedCookie = await updateResponse;
  expect(updatedCookie.ok()).toBe(true);
  expect(await updatedCookie.text()).not.toMatch(/UPDATED_(AUTH|CSRF)_SECRET/);
  await expect(authToken).toHaveValue("");
  await expect(ct0).toHaveValue("");
  await expect(remove).toBeEnabled();
  await idle(request);
  await page.reload();
  await expect(authToken).toHaveValue("");
  await expect(ct0).toHaveValue("");
  await expect(section).toContainText("有效");
  await page.screenshot({
    path: `test-results/${testInfo.project.name}-cookie-settings.png`,
    fullPage: true,
  });

  await remove.click();
  const dialog = page.locator(".semi-modal");
  await expect(dialog).toContainText("删除登录 Cookie？");
  const [deleteResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === "/api/cookies" &&
        response.request().method() === "DELETE",
      { timeout: 15000 },
    ),
    dialog.getByRole("button", { name: "confirm", exact: true }).click(),
  ]);
  expect(deleteResponse.ok()).toBe(true);
  await expect(dialog).toHaveCount(0);
  await expect(section).toContainText("未设置");
  await expect(authToken).toHaveValue("");
  await expect(ct0).toHaveValue("");
  await expect(remove).toBeDisabled();
  await expect(check).toBeDisabled();
});

test("cookie input, user sync, tweet import, media previews, tags and complete snapshot diffs", async ({
  page,
  request,
}, testInfo) => {
  await request.post("/__fixture/reset");
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/settings");
  const authToken = page.getByLabel("auth_token", { exact: true });
  const ct0 = page.getByLabel("ct0", { exact: true });
  await authToken.fill("FIXTURE_AUTH");
  await ct0.fill("FIXTURE_CSRF");
  await page.getByRole("button", { name: "保存并检测", exact: true }).click();
  await expect(authToken).toHaveValue("");
  await expect(ct0).toHaveValue("");
  await idle(request);
  await expect
    .poll(async () => (await (await request.get("/api/cookies")).json()).status)
    .toBe("valid");
  await page.goto("/users");
  await page
    .getByRole("button", { name: "添加用户", exact: true })
    .first()
    .click();
  await page.getByLabel("用户名或主页链接").fill("@archive_test");
  await page.getByRole("button", { name: "添加并同步" }).click();
  await expect(page.locator(".semi-modal")).toHaveCount(0);
  await idle(request);
  const users = await (await request.get("/api/users")).json();
  const user = users[0];
  expect(user.tweet_count).toBe(1);
  await request.post(`/api/users/${user.id}/sync`);
  await idle(request);
  expect((await (await request.get("/api/users")).json())[0].tweet_count).toBe(
    1,
  );
  await page.getByRole("button", { name: "导入推文", exact: true }).click();
  await page
    .getByLabel("推文链接")
    .fill("https://x.com/archive_test/status/1234567890123456781");
  await page.getByRole("button", { name: "开始归档" }).click();
  await expect(page).toHaveURL(/\/tasks/);
  await idle(request);
  const before = await (await request.get("/api/media")).json();
  expect(before.total).toBe(6);
  await request.post("/api/tweets", {
    data: { target: "https://x.com/archive_test/status/1234567890123456781" },
  });
  await idle(request);
  expect((await (await request.get("/api/media")).json()).total).toBe(6);
  await page.goto("/tweets");
  await expect(page.locator(".tweet-row")).toHaveCount(2);
  await expect(page.locator(".tweet-media .media-preview")).toHaveCount(6);
  await expect
    .poll(() =>
      page.locator(".tweet-media img").evaluateAll((images) =>
        images.every((image) => (image as HTMLImageElement).naturalWidth > 0),
      ),
    )
    .toBe(true);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: `test-results/${testInfo.project.name}-tweet-previews.png`,
    fullPage: true,
  });
  await page.locator(".tweet-media .media-preview").first().click();
  const modal = page.locator(".media-modal .semi-modal");
  await expect(modal).toBeVisible();
  await expect(modal.locator(".detail-navigation")).toContainText("1 / 3");
  await expect(modal.getByRole("button", { name: "上一个媒体" })).toBeDisabled();
  await modal.getByRole("button", { name: "放大原图" }).click();
  await expect(page.locator(".pswp")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.locator(".pswp")).toHaveCount(0);
  await expect(modal).toBeVisible();
  await modal.getByRole("button", { name: "下一个媒体" }).click();
  await expect(modal.locator(".detail-navigation")).toContainText("2 / 3");
  await expect(modal.locator("video")).toBeVisible();
  await modal.locator("video").evaluate((element: HTMLVideoElement) => {
    element.muted = true;
    return element.play();
  });
  await expect
    .poll(() =>
      modal
        .locator("video")
        .evaluate((element: HTMLVideoElement) => element.currentTime),
    )
    .toBeGreaterThan(0);
  const previousVideo = await modal.locator("video").elementHandle();
  await modal.getByRole("button", { name: "切换到第 3 个媒体" }).click();
  expect(await previousVideo!.evaluate((element) => element.paused)).toBe(true);
  await previousVideo!.dispose();
  await expect(modal.locator("video")).toHaveAttribute("loop", "");
  await expect
    .poll(() =>
      modal
        .locator("video")
        .evaluate((element: HTMLVideoElement) => element.paused),
    )
    .toBe(false);
  await expect(modal.getByRole("button", { name: "下一个媒体" })).toBeDisabled();
  await modal.getByRole("button", { name: "切换到第 1 个媒体" }).click();
  await expect(modal.locator(".detail-image img")).toBeVisible();
  await expect(modal.locator("video")).toHaveCount(0);
  await page.locator(".semi-modal-close").click();
  await page.goto("/media");
  await expect(page.locator(".media-card")).toHaveCount(6);
  await expect
    .poll(async () =>
      page
        .locator(".media-card img")
        .evaluateAll((images) =>
          images.every((image) => (image as HTMLImageElement).naturalWidth > 0),
        ),
    )
    .toBe(true);
  await page.screenshot({
    path: `test-results/${testInfo.project.name}-media.png`,
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "预览 @archive_test 的图片", exact: true })
    .first()
    .click();
  await expect(modal).toBeVisible();
  await expect(modal.locator(".detail-thumbnail")).toHaveCount(6);
  await expect(modal.locator(".semi-modal-content")).toHaveCSS("transform", "none");
  const preview = await modal.locator(".detail-preview").boundingBox();
  const metadata = await modal.locator(".detail-meta").boundingBox();
  const dialogBox = await modal.boundingBox();
  const viewport = page.viewportSize()!;
  expect(preview!.width).toBeGreaterThan(viewport.width * 0.6);
  expect(dialogBox!.x).toBeGreaterThanOrEqual(0);
  expect(dialogBox!.x + dialogBox!.width).toBeLessThanOrEqual(viewport.width);
  if (testInfo.project.name === "desktop") {
    expect(preview!.width).toBeGreaterThan(metadata!.width * 3);
    expect(preview!.height).toBeGreaterThan(viewport.height * 0.6);
  }
  await page.screenshot({
    path: `test-results/${testInfo.project.name}-media-preview.png`,
    fullPage: false,
  });
  await modal.getByRole("button", { name: "放大原图" }).click();
  await expect(page.locator(".pswp")).toBeVisible();
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await expect(page.locator(".pswp")).toHaveCount(0);
  await page.locator("#media-tags").fill("reference, favorite");
  await page.getByRole("button", { name: "保存标签" }).click();
  await expect(page.locator(".semi-toast")).toContainText("标签已保存");
  await page.locator("#media-tags").press("ArrowRight");
  await expect(modal.locator(".detail-image img")).toBeVisible();
  await modal.getByRole("button", { name: "切换到第 2 个媒体" }).click();
  await expect(page.locator("#media-tags")).toHaveValue("");
  await expect(modal.locator(".detail-navigation")).toContainText("2 / 6");
  await modal.getByRole("button", { name: "上一个媒体" }).click();
  await expect(page.locator("#media-tags")).toHaveValue(
    /^(reference,favorite|favorite,reference)$/,
  );
  await modal.getByRole("button", { name: "上一个媒体" }).focus();
  await page.keyboard.press("ArrowRight");
  await expect(modal.locator(".detail-navigation")).toContainText("2 / 6");
  await page.keyboard.press("ArrowLeft");
  await expect(modal.locator(".detail-navigation")).toContainText("1 / 6");
  await page
    .locator(".media-modal")
    .getByRole("button", { name: "收藏", exact: true })
    .click();
  await page.locator(".semi-modal-close").click();
  await page.getByRole("button", { name: "仅看收藏" }).click();
  await expect(page.locator(".media-card")).toHaveCount(1);
  await page.getByRole("button", { name: "清除筛选" }).click();
  await page.getByRole("button", { name: "视频", exact: true }).click();
  await expect(page.locator(".media-card")).toHaveCount(2);
  await page
    .getByRole("button", { name: "预览 @archive_test 的视频", exact: true })
    .first()
    .click();
  const video = page.locator("video");
  await expect(video).toBeVisible();
  await video.evaluate((element: HTMLVideoElement) => {
    element.muted = true;
    return element.play();
  });
  await expect
    .poll(() =>
      video.evaluate((element: HTMLVideoElement) => element.currentTime),
    )
    .toBeGreaterThan(0);
  expect(
    await video.evaluate((element: HTMLVideoElement) => element.videoWidth),
  ).toBe(640);
  await page.locator(".semi-modal-close").click();
  await page.getByRole("button", { name: "GIF", exact: true }).click();
  await page
    .getByRole("button", { name: "预览 @archive_test 的GIF", exact: true })
    .first()
    .click();
  await expect
    .poll(() =>
      page
        .locator("video")
        .evaluate((element: HTMLVideoElement) => element.currentTime),
    )
    .toBeGreaterThan(0);
  expect(
    await page
      .locator("video")
      .evaluate((element: HTMLVideoElement) => element.paused),
  ).toBe(false);
  await page.locator(".semi-modal-close").click();
  await request.post(`/api/users/${user.id}/snapshots`, {
    data: { direction: "following" },
  });
  await idle(request);
  await request.post("/__fixture/phase", { data: { phase: 2 } });
  await request.post(`/api/users/${user.id}/snapshots`, {
    data: { direction: "following" },
  });
  await idle(request);
  const snapshots = await (
    await request.get(`/api/users/${user.id}/snapshots?direction=following`)
  ).json();
  const diff = await (
    await request.get(
      `/api/snapshots/${snapshots[0].id}/diff?base=${snapshots[1].id}`,
    )
  ).json();
  expect(diff.added[0].id).toBe("103");
  expect(diff.removed[0].id).toBe("102");
  await page.goto(`/relationships?user=${user.id}`);
  await expect(page.locator(".snapshot-item")).toHaveCount(2);
  await page.getByLabel("对比基准快照").click();
  await page.locator(".semi-select-option").first().click();
  await page.getByRole("button", { name: "查看差异" }).click();
  await expect(page.getByRole("heading", { name: "新增 (1)" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "取消 (1)" })).toBeVisible();
  await request.post("/__fixture/phase", { data: { phase: 3 } });
  await request.post(`/api/users/${user.id}/snapshots`, {
    data: { direction: "following" },
  });
  await idle(request);
  await page.reload();
  await expect(page.locator(".snapshot-item")).toHaveCount(3);
  await expect(page.getByRole("button", { name: "查看差异" })).toBeDisabled();
  await expect(page.locator(".snapshot-content .semi-banner")).toContainText(
    "不能据此判断取消关系",
  );
  await page.screenshot({
    path: `test-results/${testInfo.project.name}-relationships.png`,
    fullPage: true,
  });
  expect(errors).toEqual([]);
});
