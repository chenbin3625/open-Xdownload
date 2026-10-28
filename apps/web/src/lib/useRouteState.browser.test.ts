// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { readRouteState, updateBrowserRoute } from "./useRouteState";

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
    expect(state.gallery).toEqual({ kind: "videos", user: "alice", search: "cat", page: 3 });
    expect(state.shouldReplace).toBe(false);
  });

  it("从 URL 还原任务中心的 status / page / pageSize", () => {
    setUrl("/tasks?page=2&pageSize=50&status=failed");
    expect(readRouteState()).toMatchObject({
      section: "tasks",
      jobPage: 2,
      jobPageSize: 50,
      jobStatus: "failed",
      shouldReplace: false,
    });
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
});
