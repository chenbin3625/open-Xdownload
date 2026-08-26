import type { QueryClient } from "@tanstack/react-query";
import {
  archiveScheduleQueryRoot,
  configQueryRoot,
  dashboardMetaQueryRoot,
  jobsQueryRoot,
  type AppConfig,
  type ArchiveSchedule,
  type DashboardMeta,
  type JobsPage,
} from "./api";

export type AppBootstrap = {
  jobs?: JobsPage;
  meta?: DashboardMeta;
  schedules?: ArchiveSchedule[];
  config?: AppConfig;
};

function isJobsPage(value: unknown): value is JobsPage {
  if (!value || typeof value !== "object") {
    return false;
  }
  const page = value as Partial<JobsPage>;
  return Array.isArray(page.items) && typeof page.page === "number" && typeof page.pageSize === "number";
}
export function isDashboardMeta(value: unknown): value is DashboardMeta {
  if (!value || typeof value !== "object") {
    return false;
  }
  const meta = value as Partial<DashboardMeta>;
  return Boolean(meta.stats && typeof meta.stats.total === "number");
}

function isAppConfig(value: unknown): value is AppConfig {
  return Boolean(value && typeof value === "object" && typeof (value as AppConfig).downloadDir === "string");
}

export function parseAppBootstrap(text: string | null | undefined): AppBootstrap | null {
  if (!text) {
    return null;
  }
  try {
    const parsed = JSON.parse(text) as AppBootstrap;
    if (!parsed || typeof parsed !== "object") {
      return null;
    }
    const jobs = isJobsPage(parsed.jobs) ? parsed.jobs : undefined;
    const meta = isDashboardMeta(parsed.meta) ? parsed.meta : undefined;
    const config = isAppConfig(parsed.config) ? parsed.config : undefined;
    const schedules = Array.isArray(parsed.schedules) ? parsed.schedules : undefined;
    if (!jobs && !meta && !config) {
      return null;
    }
    return { jobs, meta, schedules, config };
  } catch {
    return null;
  }
}

export function takeAppBootstrap(): AppBootstrap | null {
  if (typeof document === "undefined") {
    return null;
  }
  const node = document.getElementById("app-bootstrap");
  const parsed = parseAppBootstrap(node?.textContent);
  node?.remove();
  return parsed;
}

export function hydrateAppQueries(
  client: QueryClient,
  bootstrap: AppBootstrap,
  page: number,
  pageSize: number,
) {
  if (bootstrap.config) {
    client.setQueryData(configQueryRoot, bootstrap.config);
  }
  if (bootstrap.meta) {
    client.setQueryData(dashboardMetaQueryRoot, bootstrap.meta);
  }
  if (bootstrap.schedules) {
    client.setQueryData(archiveScheduleQueryRoot, bootstrap.schedules);
  }
  if (bootstrap.jobs && bootstrap.jobs.page === page && bootstrap.jobs.pageSize === pageSize) {
    client.setQueryData([...jobsQueryRoot, page, pageSize], bootstrap.jobs);
  }
}
