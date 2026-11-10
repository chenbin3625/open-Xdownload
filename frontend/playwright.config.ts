import { defineConfig, devices } from "@playwright/test";

const baseURL = `http://127.0.0.1:${process.env.X_ARCHIVE_TEST_PORT || "3101"}`;

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 90000,
  expect: { timeout: 15000 },
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "desktop",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1440, height: 1000 },
      },
    },
    {
      name: "mobile",
      use: { ...devices["iPhone 13"], defaultBrowserType: "chromium" },
    },
  ],
  webServer: {
    command: "cargo run --manifest-path ../Cargo.toml --example fixture_server",
    url: `${baseURL}/api/health`,
    timeout: 120000,
    reuseExistingServer: false,
  },
});
