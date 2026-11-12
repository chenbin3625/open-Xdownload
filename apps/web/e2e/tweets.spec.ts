import { test, expect, type Page } from "@playwright/test";

async function assertTweetLayout(page: Page) {
  const overflow = await page.evaluate(() => {
    const roots = [document.documentElement, document.body, document.querySelector("main"), document.querySelector(".tweet-archive")].filter(Boolean) as HTMLElement[];
    return roots.filter(node => node.scrollWidth > node.clientWidth + 2).map(node => node.className || node.tagName);
  });
  expect(overflow).toEqual([]);
  const overlapping = await page.locator(".tweet-has-media").evaluateAll(entries => entries.some(entry => {
    const copy = entry.querySelector(".tweet-copy")!.getBoundingClientRect();
    const media = entry.querySelector(".tweet-media")!.getBoundingClientRect();
    return Math.min(copy.right, media.right) - Math.max(copy.left, media.left) > 2 && Math.min(copy.bottom, media.bottom) - Math.max(copy.top, media.top) > 2;
  }));
  expect(overlapping).toBe(false);
}

test("tweet density, long text and repost filters work with archived data", async ({ page }, info) => {
  page.setDefaultTimeout(10_000);
  const en = info.project.name.endsWith("-en");
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.addInitScript(language => localStorage.setItem("open-xdownload-language", language), en ? "en" : "zh-CN");
  await page.goto("/tweets");
  const entries = page.locator(".tweet-entry");
  await expect(entries.first()).toBeVisible();
  await expect(page.getByRole("radio", { name: en ? "Compact" : "紧凑", exact: true })).toBeChecked();
  await assertTweetLayout(page);
  const compactHeight = await entries.first().evaluate(node => node.getBoundingClientRect().height);
  if (info.project.name.startsWith("desktop")) {
    expect(compactHeight).toBeLessThan(300);
    const visible = await entries.evaluateAll(nodes => nodes.filter(node => {
      const box = node.getBoundingClientRect();
      return box.top >= 0 && box.bottom <= innerHeight;
    }).length);
    expect(visible).toBeGreaterThanOrEqual(3);
  }
  const image = page.locator(".tweet-media img").first();
  await expect.poll(() => image.evaluate((node: HTMLImageElement) => node.complete && node.naturalWidth > 0)).toBe(true);
  await page.screenshot({ path: info.outputPath("tweets-compact.png"), fullPage: true });

  const expand = page.getByRole("button", { name: en ? "Show full text" : "展开全文", exact: true }).first();
  await expect(expand).toBeVisible();
  const bodyId = await expand.getAttribute("aria-controls");
  const body = page.locator(`[id="${bodyId}"]`);
  const clippedHeight = await body.evaluate(node => node.clientHeight);
  await expand.click();
  await expect(page.getByRole("button", { name: en ? "Show less" : "收起全文", exact: true })).toBeVisible();
  expect(await body.evaluate(node => node.clientHeight)).toBeGreaterThan(clippedHeight);
  await page.getByRole("button", { name: en ? "Show less" : "收起全文", exact: true }).click();

  await image.click();
  const modal = page.getByRole("dialog");
  await expect(modal).toBeVisible();
  await expect.poll(() => modal.locator("img").evaluate((node: HTMLImageElement) => node.complete && node.naturalWidth > 0)).toBe(true);
  await page.keyboard.press("Escape");
  await expect(modal).toHaveCount(0);
  const video = page.locator(".tweet-media video").first();
  await video.evaluate((node: HTMLVideoElement) => node.play());
  await expect.poll(() => video.evaluate((node: HTMLVideoElement) => node.currentTime)).toBeGreaterThan(0);
  expect(await video.evaluate((node: HTMLVideoElement) => node.videoWidth)).toBeGreaterThan(0);
  await video.evaluate((node: HTMLVideoElement) => node.pause());

  await page.getByText(en ? "Comfortable" : "舒适", { exact: true }).click();
  await expect(page).toHaveURL(/view=comfortable/);
  expect(await entries.first().evaluate(node => node.getBoundingClientRect().height)).toBeGreaterThan(compactHeight);
  await assertTweetLayout(page);
  await page.getByText(en ? "Compact" : "紧凑", { exact: true }).click();

  const all = await page.request.get("/api/archive/tweets?pageSize=1").then(response => response.json());
  const reposts = await page.request.get("/api/archive/tweets?retweets=only&pageSize=100").then(response => response.json());
  expect(reposts.pagination.total).toBeGreaterThan(0);
  await page.getByText(en ? "Reposts only" : "仅转推", { exact: true }).click();
  await expect(page).toHaveURL(/retweets=only/);
  await expect(entries).toHaveCount(Math.min(20, reposts.pagination.total));
  await assertTweetLayout(page);
  await page.screenshot({ path: info.outputPath("tweets-reposts.png"), fullPage: true });

  const hiddenResponse = page.waitForResponse(response => response.url().includes("/api/archive/tweets?") && response.url().includes("retweets=exclude") && response.ok());
  await page.getByText(en ? "Hide reposts" : "隐藏转推", { exact: true }).click();
  const hidden = await hiddenResponse.then(response => response.json());
  expect(hidden.pagination.total + reposts.pagination.total).toBe(all.pagination.total);
  expect(hidden.items.every((tweet: { retweetedTweetId?: string }) => !tweet.retweetedTweetId)).toBe(true);
  await page.reload();
  await expect(page.getByRole("radio", { name: en ? "Hide reposts" : "隐藏转推", exact: true })).toBeChecked();
  await expect(entries.first()).toBeVisible();
  await page.getByRole("button", { name: en ? "Clear filters" : "清除筛选", exact: true }).click();
  await expect(page).not.toHaveURL(/retweets=/);
  await assertTweetLayout(page);
  expect(errors).toEqual([]);
});
