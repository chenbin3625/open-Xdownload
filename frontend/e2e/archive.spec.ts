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
    ["/relationships", "关注关系"],
    ["/tweets", "推文归档"],
    ["/media", "媒体库"],
    ["/tasks", "下载任务"],
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

test("cookie import, user sync, tweet import, media previews, tags and complete snapshot diffs", async ({
  page,
  request,
}, testInfo) => {
  await request.post("/__fixture/reset");
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto("/settings");
  await page
    .getByLabel("Cookie 文件")
    .setInputFiles({
      name: "cookies.txt",
      mimeType: "text/plain",
      buffer: Buffer.from(
        "# Netscape HTTP Cookie File\n.x.com\tTRUE\t/\tTRUE\t0\tauth_token\tFIXTURE_AUTH\n.x.com\tTRUE\t/\tTRUE\t0\tct0\tFIXTURE_CSRF\n",
      ),
    });
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
  await expect(page.locator(".pswp")).toBeVisible();
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await expect(page.locator(".pswp")).toHaveCount(0);
  await page.getByRole("button", { name: "媒体详情" }).first().click();
  await page.locator("#media-tags").fill("reference, favorite");
  await page.getByRole("button", { name: "保存标签" }).click();
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
