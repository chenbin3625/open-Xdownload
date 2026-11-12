import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "e2e",
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  globalSetup: "./e2e/global-setup.ts",
  reporter: [["list"], ["html", { outputFolder: "playwright-report", open: "never" }]],
  use: {
    baseURL: process.env.OPEN_XDOWNLOAD_PREVIEW_URL ?? "http://10.10.11.3:8787",
    trace: "retain-on-failure",
    storageState: ".playwright-auth.json",
    screenshot: "only-on-failure",
    launchOptions: { args: ["--no-sandbox"] },
  },
  projects: [
    { name: "desktop-zh", use: { viewport: { width: 1440, height: 1000 }, locale: "zh-CN" } },
    { name: "desktop-en", use: { viewport: { width: 1440, height: 1000 }, locale: "en-US" } },
    { name: "tablet-zh", use: { viewport: { width: 820, height: 1180 }, locale: "zh-CN" } },
    { name: "tablet-en", use: { viewport: { width: 820, height: 1180 }, locale: "en-US" } },
    { name: "mobile-zh", use: { viewport: { width: 390, height: 844 }, locale: "zh-CN", isMobile: true, hasTouch: true } },
    { name: "mobile-en", use: { viewport: { width: 390, height: 844 }, locale: "en-US", isMobile: true, hasTouch: true } },
  ],
});
