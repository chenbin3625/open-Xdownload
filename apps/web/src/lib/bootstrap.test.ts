import { describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  archiveScheduleQueryRoot,
  configQueryRoot,
  dashboardMetaQueryRoot,
  jobsQueryRoot,
  type AppConfig,
  type DashboardMeta,
  type JobsPage,
} from "./api";
import { hydrateAppQueries, parseAppBootstrap } from "./bootstrap";

const jobs: JobsPage = { items: [], page: 1, pageSize: 20 };
const meta: DashboardMeta = {
  stats: { total: 0, active: 0, completed: 0, failed: 0 },
  failedTweetCount: 0,
};
const config: AppConfig = {
  downloadDir: "/tmp/downloads",
  maxConcurrency: 4,
  proxyUrl: "",
  autoRetryFailed: false,
  autoFollowProtected: false,
  includeNestedTweetMedia: true,
  fileNamingMode: "tweet_text",
  maxFilenameLength: 180,
  storageType: "local",
};

describe("parseAppBootstrap", () => {
  it("reads jobs, meta and config", () => {
    const parsed = parseAppBootstrap(JSON.stringify({ jobs, meta, config, schedules: [] }));
    expect(parsed).toEqual({ jobs, meta, schedules: [], config });
  });

  it("accepts settings-only config", () => {
    const parsed = parseAppBootstrap(JSON.stringify({ config }));
    expect(parsed).toEqual({ jobs: undefined, meta: undefined, schedules: undefined, config });
  });

  it("rejects invalid json", () => {
    expect(parseAppBootstrap("{")).toBeNull();
    expect(parseAppBootstrap("")).toBeNull();
    expect(parseAppBootstrap(JSON.stringify({ schedules: [] }))).toBeNull();
  });
});
describe("hydrateAppQueries", () => {
  it("seeds matching page caches and config", () => {
    const client = new QueryClient();
    hydrateAppQueries(client, { jobs, meta, schedules: [], config }, 1, 20);
    expect(client.getQueryData([...jobsQueryRoot, 1, 20])).toEqual(jobs);
    expect(client.getQueryData(dashboardMetaQueryRoot)).toEqual(meta);
    expect(client.getQueryData(archiveScheduleQueryRoot)).toEqual([]);
    expect(client.getQueryData(configQueryRoot)).toEqual(config);
  });

  it("hydrates config and meta even when jobs page mismatches", () => {
    const client = new QueryClient();
    hydrateAppQueries(client, { jobs, meta, config }, 2, 20);
    expect(client.getQueryData([...jobsQueryRoot, 2, 20])).toBeUndefined();
    expect(client.getQueryData(dashboardMetaQueryRoot)).toEqual(meta);
    expect(client.getQueryData(configQueryRoot)).toEqual(config);
  });
});
