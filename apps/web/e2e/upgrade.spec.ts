import { test, expect, type Locator, type Page } from "@playwright/test";

const tweetURL = process.env.OPEN_XDOWNLOAD_PREVIEW_TWEET ?? "https://x.com/Starlink/status/2120206875581186293";

async function navigate(page: Page, label: string) {
  const menu = page.getByRole("button", { name: /打开导航菜单|Open navigation menu/ });
  if (await menu.isVisible()) await menu.click();
  await page.getByRole("menuitem", { name: label, exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
}

async function assertLayout(page: Page) {
  await expect(page.locator(".app-header")).toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollHeight - innerHeight)).toBeLessThanOrEqual(2);
  const overflow = await page.evaluate(() => {
    const roots = [document.documentElement, document.body, document.querySelector("main"), document.querySelector(".page-content")].filter(Boolean) as HTMLElement[];
    return roots.filter(node => node.scrollWidth > node.clientWidth + 2).map(node => ({ tag: node.tagName, className: node.className, width: node.clientWidth, scroll: node.scrollWidth }));
  });
  expect(overflow).toEqual([]);
  const controls = await page.locator("button:visible, input:visible, [role=combobox]:visible").evaluateAll(elements => elements.filter(element => {
    const box = element.getBoundingClientRect();
    for (let parent = element.parentElement; parent; parent = parent.parentElement) {
      if (["auto", "scroll"].includes(getComputedStyle(parent).overflowX) && parent.scrollWidth > parent.clientWidth) return false;
    }
    return box.width > 0 && (box.left < -2 || box.right > window.innerWidth + 2);
  }).map(element => element.outerHTML.slice(0, 200)));
  expect(controls).toEqual([]);
}

async function assertWithinViewport(locator: Locator) {
  await expect.poll(() => locator.evaluate(node => {
    const box = node.getBoundingClientRect();
    return box.width > 0 && box.height > 0 && box.left >= -2 && box.top >= -2 && box.right <= innerWidth + 2 && box.bottom <= innerHeight + 2;
  })).toBe(true);
}

test.beforeEach(async ({ page }, info) => {
  const en = info.project.name.endsWith("-en");
  await page.addInitScript(language => localStorage.setItem("open-xdownload-language", language), en ? "en" : "zh-CN");
  await page.goto("/");
  await expect(page.locator(".app-shell")).toBeVisible();
});

test("archive, storage and task workflows use the real backend", async ({ page }, info) => {
  const en = info.project.name.endsWith("-en");
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await assertLayout(page);
  await navigate(page, en ? "Tweet Archive" : "推文归档");
  await expect(page.locator(".tweet-entry").first()).toBeVisible();
  await expect(page.locator(".tweet-body").first()).not.toBeEmpty();
  await expect(page.getByRole("combobox", { name: en ? "Media type" : "媒体类型", exact: true })).toContainText(en ? "All" : "全部");
  const archived = await page.request.get("/api/archive/tweets?pageSize=20").then(response => response.json());
  const longTweet = archived.items.find((tweet: { text: string }) => tweet.text.length > 280);
  expect(longTweet, "the preview must contain a real long tweet").toBeTruthy();
  await expect.poll(() => page.locator(".tweet-body").filter({ hasText: longTweet.text.slice(0, 40) }).textContent()).toBe(longTweet.text);
  await assertLayout(page);
  const search = page.getByRole("textbox", { name: en ? "Search text" : "搜索正文", exact: true });
  await search.fill("no-match-upgrade-acceptance");
  await expect(page).toHaveURL(/search=no-match-upgrade-acceptance/);
  await expect(page.locator(".tweet-entry")).toHaveCount(0);
  await search.clear();
  await expect(page.locator(".tweet-entry").first()).toBeVisible();
  const image = page.locator(".tweet-media img").first();
  await expect(image).toBeVisible();
  {
    await image.scrollIntoViewIfNeeded();
    await expect.poll(() => image.evaluate((node: HTMLImageElement) => node.complete && node.naturalWidth > 0)).toBe(true);
    await image.click();
    const preview = page.getByRole("dialog");
    await expect(preview).toBeVisible();
    await expect.poll(() => preview.evaluate(node => node.contains(document.activeElement))).toBe(true);
    await expect.poll(() => preview.locator("img").evaluate((node: HTMLImageElement) => node.complete && node.naturalWidth > 0)).toBe(true);
    for (let n = 0; n < 6; n++) { await page.keyboard.press("Tab"); expect(await preview.evaluate(node => node.contains(document.activeElement))).toBe(true); }
    await page.keyboard.press("Shift+Tab"); expect(await preview.evaluate(node => node.contains(document.activeElement))).toBe(true);
    await assertWithinViewport(preview);
    await page.screenshot({ path: info.outputPath("image-modal.png"), fullPage: true });
    await page.keyboard.press("Escape"); await expect(preview).toHaveCount(0);
    await expect(image.locator("..")).toBeFocused();
  }
  const video = page.locator(".tweet-media video").first();
  await expect(video).toBeVisible();
  {
    await video.scrollIntoViewIfNeeded();
    await video.evaluate((node: HTMLVideoElement) => node.play());
    await expect.poll(() => video.evaluate((node: HTMLVideoElement) => node.currentTime)).toBeGreaterThan(0);
    expect(await video.evaluate((node: HTMLVideoElement) => node.videoWidth)).toBeGreaterThan(0);
    await video.evaluate((node: HTMLVideoElement) => new Promise<void>(resolve => {
      node.requestVideoFrameCallback(() => resolve());
      node.currentTime = node.duration / 2;
    }));
    const poster = await page.request.get((await video.getAttribute("poster"))!);
    expect(poster.status()).toBe(200);
    expect(poster.headers()["content-type"]).toMatch(/^image\//);
    await video.screenshot({ path: info.outputPath("video-playback.png") });
    await video.evaluate((node: HTMLVideoElement) => node.pause());
  }
  const taskLink = page.locator(".tweet-entry footer button").filter({ hasText: /^#\d+$/ }).first();
  await taskLink.click();
  const drawer = page.getByRole("dialog", { name: /任务详情|Task details/ });
  await expect(drawer).toBeVisible();
  await expect.poll(() => drawer.evaluate(node => node.contains(document.activeElement))).toBe(true);
  for (let n = 0; n < 8; n++) { await page.keyboard.press("Tab"); expect(await drawer.evaluate(node => node.contains(document.activeElement))).toBe(true); }
  await page.keyboard.press("Shift+Tab"); expect(await drawer.evaluate(node => node.contains(document.activeElement))).toBe(true);
  await assertWithinViewport(drawer);
  await page.screenshot({ path: info.outputPath("task-drawer.png"), fullPage: true });
  await page.keyboard.press("Escape"); await expect(drawer).toHaveCount(0);
  await expect(taskLink).toBeFocused();
  await navigate(page, en ? "Media library" : "媒体归档");
  await expect(page.getByRole("main")).toBeVisible(); await assertLayout(page);
  const userFilter = page.getByRole("combobox", { name: en ? "Filter by user..." : "按用户筛选...", exact: true });
  await userFilter.click();
  const userSearch = page.getByRole("combobox", { name: en ? "Filter by user... Search..." : "按用户筛选... 搜索...", exact: true });
  await expect(userSearch).toBeFocused();
  const listId = await userSearch.getAttribute("aria-controls");
  expect(listId).toBeTruthy();
  await expect(page.locator(`[id="${listId}"]`)).toHaveAttribute("role", "listbox");
  await userSearch.fill("no-match-upgrade-filter");
  await expect(page.getByText(en ? "No matches" : "无匹配项", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(userSearch).toHaveCount(0);
  await expect(userFilter).toBeFocused();
  await navigate(page, en ? "Schedules" : "归档计划");
  await assertLayout(page);
  await navigate(page, en ? "Settings" : "系统配置");
  await expect(page.getByRole("heading", { name: en ? "Asset Storage" : "资产分类存储", exact: true })).toBeVisible();
  await assertLayout(page);
  const pathPreview = page.getByRole("button", { name: en ? "Preview path Original images" : "预览实际路径 图片原文件", exact: true });
  await pathPreview.scrollIntoViewIfNeeded(); await pathPreview.click();
  await expect(page.locator(".storage-path-preview").first()).toContainText(/images/);
  const addTarget = page.getByRole("button", { name: en ? "Add target" : "添加存储目标", exact: true });
  await addTarget.click();
  const targetModal = page.getByRole("dialog"); await expect(targetModal).toBeVisible();
  await expect(targetModal.getByRole("textbox", { name: en ? "Target ID" : "目标 ID", exact: true })).toBeVisible();
  for (let n = 0; n < 12; n++) { await page.keyboard.press("Tab"); expect(await targetModal.evaluate(node => node.contains(document.activeElement))).toBe(true); }
  await page.keyboard.press("Shift+Tab"); expect(await targetModal.evaluate(node => node.contains(document.activeElement))).toBe(true);
  await assertWithinViewport(targetModal);
  await page.screenshot({ path: info.outputPath("storage-modal.png"), fullPage: true });
  await page.keyboard.press("Escape"); await expect(targetModal).toHaveCount(0);
  await expect(addTarget).toBeFocused();
  if (info.project.name === "desktop-zh") {
    const storage = page.locator(".semi-settings-section").filter({ has: page.getByRole("heading", { name: "资产分类存储", exact: true }) });
    const quality = storage.getByRole("spinbutton", { name: "质量", exact: true });
    const original = await quality.inputValue();
    const changed = original === "83" ? "82" : "83";
    try {
      await quality.fill(changed);
      const saved = page.waitForResponse(response => response.url().endsWith("/api/storage") && response.request().method() === "PUT");
      await storage.getByRole("button", { name: "保存配置", exact: true }).click();
      expect((await saved).ok()).toBe(true);
      await page.reload();
      await expect(quality).toHaveValue(changed);
    } finally {
      await quality.fill(original);
      const restored = page.waitForResponse(response => response.url().endsWith("/api/storage") && response.request().method() === "PUT");
      await storage.getByRole("button", { name: "保存配置", exact: true }).click();
      expect((await restored).ok()).toBe(true);
    }
  }
  await page.screenshot({ path: info.outputPath("settings.png"), fullPage: true });
  await page.locator(".semi-settings-section").last().evaluate(node => node.scrollIntoView({ block: "end" }));
  await assertWithinViewport(page.locator(".semi-settings-section").last().getByRole("button").last());
  await assertLayout(page);
  await page.screenshot({ path: info.outputPath("settings-bottom.png"), fullPage: true });
  await navigate(page, en ? "Tasks" : "任务中心");
  const create = page.getByRole("button", { name: /新建任务|New task/ }).first();
  await create.click();
  const modal = page.getByRole("dialog"); await expect(modal).toBeVisible();
  for (const key of ["Tab", "Tab", "Shift+Tab"]) { await page.keyboard.press(key); expect(await modal.evaluate(node => node.contains(document.activeElement))).toBe(true); }
  await assertWithinViewport(modal);
  await page.screenshot({ path: info.outputPath("create-modal.png"), fullPage: true });
  await page.keyboard.press("Escape"); await expect(modal).toHaveCount(0);
  await expect(create).toBeFocused();
  await create.click(); await expect(modal).toBeVisible();
  await modal.getByRole("tab", { name: /单条推文|Single tweet/ }).click();
  await modal.getByRole("combobox", { name: en ? "Archive content" : "归档内容", exact: true }).click();
  await page.getByRole("option", { name: en ? "Tweets only" : "仅推文", exact: true }).click();
  await modal.getByPlaceholder(/https:\/\/x.com\/username\/status/).fill(tweetURL);
  await modal.getByRole("button", { name: en ? "Download now" : "立即下载", exact: true }).click();
  await expect(modal).toHaveCount(0);
  const success = page.locator(".semi-toast").last();
  await expect(success).toBeVisible();
  await assertWithinViewport(success);
  await assertLayout(page);
  await page.screenshot({ path: info.outputPath("tasks.png"), fullPage: true });
  if (!info.project.name.startsWith("mobile")) {
    const row = page.getByRole("treegrid").locator("tbody tr").first();
    const actions = row.locator("td").last();
    await actions.scrollIntoViewIfNeeded();
    await assertWithinViewport(actions);
    await page.screenshot({ path: info.outputPath("task-actions.png"), fullPage: true });
  }
  await navigate(page, en ? "Tweet Archive" : "推文归档");
  const archiveURL = page.url();
  await page.reload();
  await expect(page).toHaveURL(archiveURL);
  await expect(page.locator(".tweet-entry").first()).toBeVisible();
  await page.screenshot({ path: info.outputPath("tweets.png"), fullPage: true });
  expect(errors).toEqual([]);
});

test("recurring batches preserve archive mode and support pause and delete", async ({ page }, info) => {
  const en = info.project.name.endsWith("-en");
  const name = `acceptance-${info.project.name}-${Date.now()}`;
  await navigate(page, en ? "Schedules" : "归档计划");
  await page.getByRole("button", { name: en ? "New archive schedule" : "新建归档计划", exact: true }).click();
  const modal = page.getByRole("dialog");
  const mode = modal.getByRole("combobox", { name: en ? "Archive content" : "归档内容", exact: true });
  await mode.click();
  await page.getByRole("option", { name: en ? "Tweets only" : "仅推文", exact: true }).click();
  await modal.getByRole("textbox", { name: en ? "User archive" : "用户归档", exact: true }).fill("jack\n@Starlink\njack");
  await modal.getByRole("tab", { name: /列表归档|List archive/ }).click();
  await expect(mode).toContainText(en ? "Tweets only" : "仅推文");
  await modal.getByRole("tab", { name: /用户归档|User archive/ }).click();
  await expect(modal.getByRole("textbox", { name: en ? "User archive" : "用户归档", exact: true })).toHaveValue("jack\n@Starlink\njack");
  await expect(modal.getByRole("checkbox", { name: /是否保存为自动归档计划|Save as a recurring archive schedule/ })).toBeChecked();
  await modal.getByRole("textbox", { name: en ? "Schedule name" : "计划名称", exact: true }).fill(name);
  await modal.getByRole("spinbutton", { name: /调度频率|Interval \(minutes\)/ }).fill("5");
  const saved = page.waitForResponse(response => response.url().endsWith("/api/archive-schedules") && response.request().method() === "POST");
  await modal.getByRole("button", { name: /保存定时计划|Save schedule/, exact: true }).click();
  const response = await saved;
  expect(response.ok()).toBe(true);
  const schedule = await response.json();
  expect(schedule.items).toHaveLength(2);
  expect(schedule.items.every((item: { archiveMode: string }) => item.archiveMode === "tweets")).toBe(true);
  await expect(modal).toHaveCount(0);
  const card = page.locator(".semi-card").filter({ has: page.getByText(name, { exact: true }) });
  await expect(card).toBeVisible();
  await assertLayout(page);
  const toggle = card.getByRole("switch");
  await expect(toggle).toBeChecked();
  const paused = page.waitForResponse(response => response.url().endsWith(`/api/archive-schedules/${schedule.id}`) && response.request().method() === "PUT");
  await toggle.click();
  await expect(toggle).not.toBeChecked();
  const state = await paused.then(result => result.json());
  expect(state.items.every((item: { archiveMode: string }) => item.archiveMode === "tweets")).toBe(true);
  await card.getByRole("button", { name: en ? "Delete schedule" : "删除计划", exact: true }).click();
  const confirm = page.getByRole("alertdialog");
  await expect(confirm).toBeVisible();
  await confirm.getByRole("button", { name: en ? "Delete" : "删除", exact: true }).click();
  await expect(card).toHaveCount(0);
});

test("stored bodies and assets remain available while X is unreachable", async ({ page }, info) => {
  const original = await page.request.get("/api/config").then(response => response.json());
  const headers = { "X-XDownload-Request": "1" };
  try {
    const changed = await page.request.put("/api/config", { headers, data: { proxyUrl: "http://127.0.0.1:1" } });
    expect(changed.ok(), await changed.text()).toBe(true);
    await navigate(page, info.project.name.endsWith("-en") ? "Tweet Archive" : "推文归档");
    await expect(page.locator(".tweet-entry").first()).toBeVisible();
    const archive = await page.request.get("/api/archive/tweets?pageSize=20").then(response => response.json());
    const tweet = archive.items.find((item: { downloads: unknown[] }) => item.downloads.length > 0);
    expect(tweet).toBeTruthy();
    const file = await page.request.get(tweet.downloads[0].fileUrl, { headers: { Range: "bytes=0-31" } });
    expect(file.status()).toBe(206);
    expect((await file.body()).length).toBe(32);
    const retry = await page.request.post(`/api/archive/tweets/${tweet.id}/retry-media`, { headers }).then(response => response.json());
    await expect.poll(async () => page.request.get(`/api/jobs/${retry.id}`).then(response => response.json()).then(job => job.status), { timeout: 20_000 }).toBe("completed");
    const after = await page.request.get(`/api/archive/tweets/${tweet.id}`).then(response => response.json());
    expect(after.text).toBe(tweet.text);
    await assertLayout(page);
  } finally {
    const restored = await page.request.put("/api/config", { headers, data: { proxyUrl: original.proxyUrl } });
    expect(restored.ok(), await restored.text()).toBe(true);
  }
});

test("polling continues after the event stream is unavailable", async ({ page }, info) => {
  await page.route("**/api/events**", route => route.abort("connectionfailed"));
  const polls: number[] = [];
  page.on("response", response => { if (/\/api\/jobs\?/.test(response.url()) && response.ok()) polls.push(Date.now()); });
  await page.reload();
  await expect(page.locator(".app-shell")).toBeVisible();
  await expect.poll(() => polls.length).toBeGreaterThan(0);
  const loadedAt = Date.now();
  if (!info.project.name.startsWith("mobile")) await expect(page.getByRole("button", { name: /实时更新已断开|Live updates disconnected/ })).toBeVisible();
  await expect.poll(() => polls.filter(time => time > loadedAt + 4000).length, { timeout: 40_000, intervals: [1000] }).toBeGreaterThan(0);
  await assertLayout(page);
  await page.screenshot({ path: info.outputPath("poll-fallback.png"), fullPage: true });
});
