// @vitest-environment jsdom
import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { buildRoutePath, defaultGalleryFilter, readRouteState, updateBrowserRoute, useRouteState } from "./useRouteState";

function setUrl(path: string) {
  window.history.replaceState(null, "", path);
}

describe("URL 路由状态往返", () => {
  afterEach(() => setUrl("/"));

  it("任务中心默认展示第一页最近 10 条", () => {
    setUrl("/tasks");
    expect(readRouteState()).toMatchObject({ jobPage: 1, jobPageSize: 10, shouldReplace: false });
  });

  it("从 URL 还原媒体库筛选（刷新/分享）", () => {
    setUrl("/gallery?page=3&kind=videos&user=alice&q=cat");
    const state = readRouteState();
    expect(state.section).toBe("gallery");
    expect(state.gallery).toEqual({ ...defaultGalleryFilter, kind: "videos", user: "alice", search: "cat", page: 3 });
    expect(state.shouldReplace).toBe(false);
  });

  it("从 URL 还原任务中心的 status / page / pageSize", () => {
    setUrl("/tasks?page=2&pageSize=50&status=failed&kind=user&q=alice");
    expect(readRouteState()).toMatchObject({
      section: "tasks",
      jobPage: 2,
      jobPageSize: 50,
      jobStatus: "failed",
      jobKind: "user",
      jobSearch: "alice",
      shouldReplace: false,
    });
  });

  it("往返保存媒体库排序、日期和体积区间", () => {
    setUrl("/gallery?sort=size_asc&from=2020-06-01&to=2020-06-30&minBytes=0&maxBytes=1048576");
    const state = readRouteState();
    expect(state.gallery).toMatchObject({ sort: "size_asc", from: "2020-06-01", to: "2020-06-30", minBytes: "0", maxBytes: "1048576" });
    expect(state.shouldReplace).toBe(false);
    updateBrowserRoute({ section: "gallery", gallery: { ...state.gallery, page: 2 } });
    expect(readRouteState().gallery).toEqual({ ...state.gallery, page: 2 });
  });

  it("清理非法媒体库区间和排序", () => {
    setUrl("/gallery?sort=bad&from=2020-02-30&to=bad&minBytes=-1&maxBytes=NaN");
    expect(readRouteState().gallery).toEqual(defaultGalleryFilter);
    expect(readRouteState().shouldReplace).toBe(true);
  });

  it("非法或冗余参数会触发一次 replace 规范化", () => {
    setUrl("/gallery?kind=bogus&user=all&q=");
    expect(readRouteState().shouldReplace).toBe(true);
  });

  it("把状态写回 URL 并可原样读回", () => {
    setUrl("/gallery");
    updateBrowserRoute({ section: "gallery", gallery: { kind: "images", page: 2 } });
    expect(`${window.location.pathname}${window.location.search}`).toBe(
      "/gallery?page=2&kind=images",
    );
    const state = readRouteState();
    expect(state.gallery.kind).toBe("images");
    expect(state.gallery.page).toBe(2);
  });

  it("replace 模式不新增历史记录（搜索逐字输入用）", () => {
    setUrl("/gallery?q=a");
    const before = window.history.length;
    updateBrowserRoute({ section: "gallery", gallery: { search: "ab" } }, true);
    expect(window.location.search).toBe("?q=ab");
    expect(window.history.length).toBe(before);
  });

  it("未保存设置阻止后退离开，也允许确认后前进/后退", () => {
    setUrl("/settings");
    const { result } = renderHook(() => useRouteState());
    const protect = (event: Event) => event.preventDefault();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    window.addEventListener("settings:before-navigate", protect);
    const pushSpy = vi.spyOn(window.history, "pushState");
    const replaceSpy = vi.spyOn(window.history, "replaceState");
    act(() => {
      setUrl("/tasks");
      replaceSpy.mockClear();
      pushSpy.mockClear();
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(result.current.activeSection).toBe("settings");
    expect(window.location.pathname).toBe("/settings");
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(pushSpy).toHaveBeenCalledWith(null, "", "/settings");
    expect(replaceSpy).not.toHaveBeenCalled();
    pushSpy.mockRestore();
    replaceSpy.mockRestore();
    confirm.mockReturnValue(true);
    act(() => { setUrl("/tasks"); window.dispatchEvent(new PopStateEvent("popstate")); });
    expect(result.current.activeSection).toBe("tasks");
    window.removeEventListener("settings:before-navigate", protect);
    act(() => { setUrl("/settings"); window.dispatchEvent(new PopStateEvent("popstate")); });
    expect(result.current.activeSection).toBe("settings");
    confirm.mockRestore();
  });

  it("preserves query params for /tweets/ with a trailing slash", () => {
    setUrl("/tweets/?author=alice&sort=asc");
    expect(buildRoutePath({ section: "tweets" })).toBe("/tweets?author=alice&sort=asc");
    expect(readRouteState().shouldReplace).toBe(true);
    updateBrowserRoute({ section: "tweets" }, true);
    expect(`${window.location.pathname}${window.location.search}`).toBe("/tweets?author=alice&sort=asc");
  });

  it("clamps gallery search in handleGalleryChange and clears job filters atomically", () => {
    setUrl("/gallery");
    const { result: galleryHook } = renderHook(() => useRouteState());
    act(() => {
      galleryHook.current.handleGalleryChange({ search: "a".repeat(200), page: 1 });
    });
    expect(galleryHook.current.gallery.search).toHaveLength(120);
    expect(new URLSearchParams(window.location.search).get("q")).toHaveLength(120);

    setUrl("/tasks?page=2&status=failed&kind=user&q=alice");
    const { result: tasksHook } = renderHook(() => useRouteState());
    const historyBefore = window.history.length;
    act(() => {
      tasksHook.current.clearJobFilters();
    });
    expect(tasksHook.current.jobStatus).toBe("all");
    expect(tasksHook.current.jobKind).toBe("all");
    expect(tasksHook.current.jobSearch).toBe("");
    expect(tasksHook.current.jobPage).toBe(1);
    expect(`${window.location.pathname}${window.location.search}`).toBe("/tasks");
    expect(window.history.length - historyBefore).toBeLessThanOrEqual(1);
  });
});
