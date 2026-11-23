import path from "node:path";
import { fileURLToPath } from "node:url";
import { request, type FullConfig } from "@playwright/test";

const e2eDir = path.dirname(fileURLToPath(import.meta.url));
const storageStatePath = path.resolve(e2eDir, "../.playwright-auth.json");

export default async function setup(config: FullConfig) {
  const context = await request.newContext({ baseURL: config.projects[0].use.baseURL });
  try {
    const state = await context.get("/api/auth/session").then(response => response.json());
    const response = await context.post(state.configured ? "/api/auth/login" : "/api/auth/setup", {
      data: { username: process.env.OPEN_XDOWNLOAD_PREVIEW_USER ?? "preview", password: process.env.OPEN_XDOWNLOAD_PREVIEW_PASSWORD ?? "UpgradePreview2026!" },
      headers: { "X-XDownload-Request": "1" },
    });
    if (!response.ok()) throw new Error(`Preview authentication failed: HTTP ${response.status()} ${await response.text()}`);
    await context.storageState({ path: storageStatePath });
  } finally { await context.dispose(); }
}
