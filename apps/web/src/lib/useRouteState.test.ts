import { describe, expect, it } from "vitest";
import {
  buildRoutePath,
  defaultGalleryFilter,
  normalizePathname,
  parseGalleryKind,
  parseJobPageSize,
  parsePositiveInteger,
  parseSearchKeyword,
  parseStatusFilter,
  readRouteState,
  sectionRoutes,
  updateBrowserRoute,
} from "./useRouteState";

describe("useRouteState helpers", () => {
  it("normalizes trailing slashes", () => {
    expect(normalizePathname("/")).toBe("/");
    expect(normalizePathname("/overview/")).toBe("/overview");
    // 该函数只去除尾部斜杠，不折叠前导双斜杠（浏览器不会产生此类 pathname）。
    expect(normalizePathname("//settings///")).toBe("//settings");
  });

  it("parses positive integers with fallback", () => {
    expect(parsePositiveInteger("3", 1)).toBe(3);
    expect(parsePositiveInteger("0", 1)).toBe(1);
    expect(parsePositiveInteger("-2", 1)).toBe(1);
    expect(parsePositiveInteger("abc", 5)).toBe(5);
    expect(parsePositiveInteger(null, 5)).toBe(5);
  });

  it("restricts page sizes to known options", () => {
    expect(parseJobPageSize("20")).toBe(20);
    expect(parseJobPageSize("100")).toBe(100);
    expect(parseJobPageSize("15")).toBe(20); // 不在选项内，回落默认
  });

  it("sanitizes status / kind / search query values", () => {
    expect(parseStatusFilter("failed")).toBe("failed");
    expect(parseStatusFilter("bogus")).toBe("all");
    expect(parseStatusFilter(null)).toBe("all");
    expect(parseGalleryKind("videos")).toBe("videos");
    expect(parseGalleryKind("bogus")).toBe("all");
    expect(parseSearchKeyword("hi")).toBe("hi");
    expect(parseSearchKeyword(null)).toBe("");
    expect(parseSearchKeyword("x".repeat(500))).toHaveLength(120);
  });

  it("builds route paths with query params", () => {
    expect(buildRoutePath({ section: "overview" })).toBe("/overview");
    expect(buildRoutePath({ section: "overview", jobPage: 3 })).toBe("/overview?page=3");
    expect(buildRoutePath({ section: "overview", jobPage: 3, jobPageSize: 50 })).toBe(
      "/overview?page=3&pageSize=50",
    );
    expect(buildRoutePath({ section: "tasks", jobStatus: "failed" })).toBe("/tasks?status=failed");
    expect(buildRoutePath({ section: "settings" })).toBe("/settings");
    // 设置页不携带任务分页/筛选参数
    expect(buildRoutePath({ section: "settings", jobPage: 3, jobPageSize: 50, jobStatus: "failed" })).toBe(
      "/settings",
    );
    // 定时计划页也不带
    expect(buildRoutePath({ section: "schedules", jobPage: 2 })).toBe("/schedules");
  });

  it("builds gallery routes with kind/user/search/page", () => {
    expect(buildRoutePath({ section: "gallery" })).toBe("/gallery");
    expect(
      buildRoutePath({
        section: "gallery",
        gallery: { kind: "videos", user: "alice", search: "日落", page: 2 },
      }),
    ).toBe("/gallery?page=2&kind=videos&user=alice&q=%E6%97%A5%E8%90%BD");
    // 默认筛选不写进 URL
    expect(buildRoutePath({ section: "gallery", gallery: { ...defaultGalleryFilter } })).toBe("/gallery");
  });

  it("keeps section route map complete", () => {
    expect(sectionRoutes.overview).toBe("/overview");
    expect(sectionRoutes.settings).toBe("/settings");
  });
});

describe("readRouteState without window", () => {
  it("falls back to defaults in a non-browser environment", () => {
    // node 测试环境（无 jsdom）没有 window：应返回默认路由状态。
    if (typeof window !== "undefined") {
      return; // 浏览器侧由 useRouteState.browser.test.ts 覆盖
    }
    const state = readRouteState();
    expect(state.section).toBe("overview");
    expect(state.jobPage).toBe(1);
    expect(state.jobPageSize).toBe(20);
    expect(state.jobStatus).toBe("all");
    expect(state.gallery).toEqual(defaultGalleryFilter);
  });
});

describe("updateBrowserRoute", () => {
  it("no-ops without a real window", () => {
    // node 环境 window/history 存在但不完整；updateBrowserRoute 需要 history API。
    expect(() => updateBrowserRoute({ section: "overview" }, true)).not.toThrow();
  });
});
