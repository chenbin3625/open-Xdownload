import { describe, expect, it } from "vitest";
import {
  buildRoutePath,
  normalizePathname,
  parseJobPageSize,
  parsePositiveInteger,
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

  it("builds route paths with query params", () => {
    expect(buildRoutePath("overview")).toBe("/overview");
    expect(buildRoutePath("overview", 3)).toBe("/overview?page=3");
    expect(buildRoutePath("overview", 3, 50)).toBe("/overview?page=3&pageSize=50");
    expect(buildRoutePath("settings")).toBe("/settings");
    // 设置页不携带任务分页参数
    expect(buildRoutePath("settings", 3, 50)).toBe("/settings");
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
      return; // 若外层 env 提供 window，交由浏览器侧人工验证
    }
    const state = readRouteState();
    expect(state.section).toBe("overview");
    expect(state.jobPage).toBe(1);
    expect(state.jobPageSize).toBe(20);
  });
});

describe("updateBrowserRoute", () => {
  it("no-ops without a real window", () => {
    // node 环境 window/history 存在但不完整；updateBrowserRoute 需要 history API。
    expect(() => updateBrowserRoute("overview", 1, 20, true)).not.toThrow();
  });
});
