import { test, expect } from "@playwright/test";
import english from "../src/locales/en.json" with { type: "json" };
import chinese from "../src/locales/zh-CN.json" with { type: "json" };

test("media folders, ordered filenames and per-kind previews persist with the real backend", async ({ page }, info) => {
  const en = info.project.name.endsWith("-en");
  const text = (key: string) => ((en ? english : chinese) as Record<string, string>)[key] ?? key;
  const labels = en ? ["image", "video", "thumbnail", "video_cover"].map(kind => text(`storage.kind.${kind}`)) : ["图片原文件", "视频及 GIF 原文件", "缩略图", "视频封面"];
  const current = await page.request.get("/api/storage").then(response => response.json());
  const setup = await page.request.put("/api/storage", { data: {
    ...current, enabled: true,
    default: { ...current.default, prefix: "", directoryTemplate: "{username}", directoryTemplateSet: true, filenameTemplate: "{media_id}.{ext}", separateFolder: false },
    rules: {}, thumbnail: { ...current.thumbnail, format: "png" },
  } });
  expect(setup.ok()).toBe(true);
  await page.addInitScript(language => localStorage.setItem("open-xdownload-language", language), en ? "en" : "zh-CN");
  await page.goto("/settings");
  const image = page.getByRole("group", { name: labels[0], exact: true });
  await image.getByRole("switch", { name: `${labels[0]} ${text("单独设置")}`, exact: true }).click();
  await image.getByRole("switch", { name: `${labels[0]} ${text("按媒体类型分文件夹")}`, exact: true }).click();
  await image.getByRole("combobox", { name: `${labels[0]} ${text("分类存储策略")}`, exact: true }).click();
  await page.getByRole("option", { name: text("全部放在同一目录"), exact: true }).click();
  await image.getByRole("combobox", { name: `${labels[0]} ${text("命名策略")}`, exact: true }).click();
  await page.getByRole("option", { name: text("自定义组合"), exact: true }).click();
  const add = image.getByRole("combobox", { name: `${labels[0]} ${text("添加命名字段")}`, exact: true });
  for (const field of ["正文", "正文编号"]) {
    await add.click();
    await page.getByRole("option", { name: text(field), exact: true }).click();
  }
  await image.getByRole("button", { name: `${labels[0]} ${text("正文")} ${text("上移")}`, exact: true }).click();
  const save = page.getByRole("button", { name: text("保存存储设置"), exact: true });
  await save.click();
  await expect(save).toBeDisabled();
  const saved = await page.request.get("/api/storage").then(response => response.json());
  expect(saved.rules.image).toMatchObject({ separateFolder: true, directoryTemplate: "", directoryTemplateSet: true, filenameTemplate: "{text}_{media_id}_{tweet_id}.{ext}" });

  await page.reload();
  await expect(image.getByRole("combobox", { name: `${labels[0]} ${text("命名策略")}`, exact: true })).toContainText(text("自定义组合"));
  for (const [index, kind] of ["image", "video", "thumbnail", "video_cover"].entries()) {
    const row = page.getByRole("group", { name: labels[index], exact: true });
    const response = page.waitForResponse(response => response.url().endsWith("/api/storage/preview") && response.request().method() === "POST");
    await row.getByRole("button", { name: `${labels[index]} ${text("storage.preview")}`, exact: true }).click();
    const result = await response;
    expect(result.ok()).toBe(true);
    const input = result.request().postDataJSON();
    expect(input.kind).toBe(kind);
    const preview = row.locator("output");
    await expect(preview).not.toBeEmpty();
    await expect(preview).toContainText(index === 0 ? "/image/" : `/${input.tweet.author.screenName}/`);
    await expect(preview).toContainText(kind === "video" ? ".mp4" : kind === "thumbnail" ? ".png" : ".jpg");
  }
  const longPreview = await page.request.post("/api/storage/preview", { data: {
    document: saved, kind: "image", index: 0, extension: "jpg",
    tweet: { id: "2120206875581186293", text: "中文😀正文".repeat(100), createdAt: "2026-11-10T12:00:00Z", author: { id: "42", name: "Author", screenName: "author" }, media: [] },
    media: { id: "1920206875581186293", type: "photo" },
  } });
  expect(longPreview.ok()).toBe(true);
  const key = (await longPreview.json()).key;
  const filename = key.split("/").at(-1);
  expect(Buffer.byteLength(filename, "utf8")).toBeLessThanOrEqual(240);
  expect(filename).toMatch(/_1920206875581186293_2120206875581186293\.jpg$/);
  const overflow = await page.locator(".storage-settings").evaluate(root => [...root.querySelectorAll("button, input, [role=combobox]")].filter(node => {
    const rect = node.getBoundingClientRect();
    return rect.width > 0 && (rect.left < 0 || rect.right > innerWidth + 1);
  }).map(node => node.outerHTML.slice(0, 200)));
  expect(overflow).toEqual([]);
  await page.locator(".storage-settings").screenshot({ path: info.outputPath("storage-policies.png") });
});
