// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React, { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "../components/ui/Overlay";
import type { DownloadRecord } from "../lib/api";
import { defaultGalleryFilter, type GalleryRouteState } from "../lib/useRouteState";
import { GalleryPage } from "./GalleryPage";

const records: DownloadRecord[] = [
  {
    id: 1, jobId: 1, tweetId: "original", mediaUrl: "https://pbs.twimg.com/media/shared.jpg",
    filePath: "/downloads/shared.jpg", bytes: 100, createdAt: "2026-09-01T00:00:00Z",
    userScreenName: "alice", userName: "Alice",
  },
  {
    id: 2, jobId: 2, tweetId: "retweet", mediaUrl: "https://pbs.twimg.com/media/shared?format=jpg",
    filePath: "/downloads/shared.jpg", bytes: 100, createdAt: "2026-09-02T00:00:00Z",
    userScreenName: "bob", userName: "Bob",
  },
  {
    id: 3, jobId: 3, tweetId: "other", mediaUrl: "https://pbs.twimg.com/media/other.jpg",
    filePath: "/downloads/other.jpg", bytes: 90, createdAt: "2026-09-03T00:00:00Z",
  },
];

const mediaRecords: DownloadRecord[] = [
  {
    id: 11, jobId: 1, tweetId: "v1", mediaUrl: "https://video.twimg.com/a.mp4",
    filePath: "/downloads/clip-a.mp4", bytes: 100, createdAt: "2026-09-01T00:00:00Z",
  },
  {
    id: 12, jobId: 1, tweetId: "v2", mediaUrl: "https://video.twimg.com/b.mp4",
    filePath: "/downloads/clip-b.mp4", bytes: 100, createdAt: "2026-09-02T00:00:00Z",
  },
];

type PageMeta = {
  page?: number;
  total?: number;
  totalPages?: number;
  counts?: { all: number; images: number; videos: number; gifs: number };
  users?: { value: string; label: string }[];
  truncated?: boolean;
};

function pagedResponse(items: DownloadRecord[], meta: PageMeta = {}) {
  const page = meta.page ?? 1;
  const total = meta.total ?? items.length;
  const counts = meta.counts ?? {
    all: total,
    images: items.filter((item) => !item.filePath.endsWith(".mp4")).length,
    videos: items.filter((item) => item.filePath.endsWith(".mp4")).length,
    gifs: 0,
  };
  return new Response(
    JSON.stringify({
      items,
      pagination: {
        page,
        pageSize: 60,
        total,
        totalPages: meta.totalPages ?? Math.max(1, Math.ceil(total / 60)),
      },
      counts,
      users: meta.users ?? [],
      truncated: meta.truncated ?? false,
    }),
    { headers: { "Content-Type": "application/json" } },
  );
}

function requestParams(input: RequestInfo | URL) {
  return new URL(String(input), "http://localhost").searchParams;
}

function renderGallery(props: { filter?: GalleryRouteState; onFilterChange?: (patch: Partial<GalleryRouteState>, options?: { replace?: boolean }) => void } = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <GalleryPage {...props} />
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

/** 模拟 App 里的受控用法：onFilterChange 写回 filter（真实实现写的是 URL）。 */
function ControlledGallery({
  initial,
  onFilterChange,
}: {
  initial?: Partial<GalleryRouteState>;
  onFilterChange?: (patch: Partial<GalleryRouteState>, options?: { replace?: boolean }) => void;
}) {
  const [filter, setFilter] = useState<GalleryRouteState>({ ...defaultGalleryFilter, ...initial });
  return (
    <GalleryPage
      filter={filter}
      onFilterChange={(patch, options) => {
        onFilterChange?.(patch, options);
        setFilter((current) => ({ ...current, ...patch }));
      }}
    />
  );
}

function renderControlled(initial?: Partial<GalleryRouteState>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onFilterChange = vi.fn();
  render(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <ControlledGallery initial={initial} onFilterChange={onFilterChange} />
      </TooltipProvider>
    </QueryClientProvider>,
  );
  return { onFilterChange };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

it("dedupes one card per saved file (server 已按 filePath 去重，前端再兜一层)", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => pagedResponse(records)));
  renderGallery();

  await waitFor(() => expect(screen.getAllByText("shared.jpg")).toHaveLength(1));
  expect(screen.getByText("other.jpg")).toBeTruthy();
});

it("把搜索词交给服务端分页接口，而不是本地过滤", async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const search = requestParams(input).get("search") ?? "";
    const items = search ? records.filter((item) => item.tweetId.includes(search)) : records;
    return pagedResponse(items);
  });
  vi.stubGlobal("fetch", fetchMock);
  renderGallery();

  await waitFor(() => expect(screen.getByText("other.jpg")).toBeTruthy());
  await userEvent.type(screen.getByPlaceholderText("搜索文件名、推文 ID..."), "retweet");

  await waitFor(() => {
    const searched = fetchMock.mock.calls.some(
      ([input]) => requestParams(input).get("search") === "retweet",
    );
    expect(searched).toBe(true);
  });
  await waitFor(() => expect(screen.queryByText("other.jpg")).toBeNull());
});

it("搜索输入有 300ms 防抖（避免每敲一个字符打一次请求）", async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const search = requestParams(input).get("search") ?? "";
    return pagedResponse(search ? records.filter((item) => item.filePath.includes(search)) : records);
  });
  vi.stubGlobal("fetch", fetchMock);
  renderGallery();

  await waitFor(() => expect(screen.getByText("shared.jpg")).toBeTruthy());
  const callsBefore = fetchMock.mock.calls.length;
  const input = screen.getByPlaceholderText("搜索文件名、推文 ID...");
  await userEvent.type(input, "shared");

  // 防抖窗口内不应产生新的搜索请求（首个字符立即触发一次计时，但不会立刻发请求）
  const intermediateCalls = fetchMock.mock.calls
    .slice(callsBefore)
    .filter(([request]) => (requestParams(request).get("search") ?? "") !== "");
  expect(intermediateCalls.length).toBeLessThanOrEqual(1);

  await waitFor(
    () => {
      const searched = fetchMock.mock.calls.some(
        ([request]) => requestParams(request).get("search") === "shared",
      );
      expect(searched).toBe(true);
    },
    { timeout: 2_000 },
  );
});

it("翻页时保留上一页内容（placeholderData），不闪骨架屏", async () => {
  const gate: { release?: () => void } = {};
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const page = Number(requestParams(input).get("page") ?? "1");
    if (page === 2) {
      await new Promise<void>((resolve) => {
        gate.release = resolve;
      });
      return pagedResponse(
        [{ ...records[2], id: 30, filePath: "/downloads/page-2.jpg" }],
        { page: 2, total: 121 },
      );
    }
    return pagedResponse(records, { total: 121 });
  });
  vi.stubGlobal("fetch", fetchMock);
  renderControlled();

  await waitFor(() => expect(screen.getByText("shared.jpg")).toBeTruthy());
  await userEvent.click(screen.getByRole("button", { name: "第 2 页" }));

  // 第 2 页还在飞：上一页内容仍在，骨架屏不出现
  expect(screen.getByText("shared.jpg")).toBeTruthy();
  expect(document.querySelectorAll(".animate-pulse").length).toBe(0);
  gate.release?.();
  await waitFor(() => expect(screen.getByText("page-2.jpg")).toBeTruthy());
});

it("切换分类/用户时同步把页码重置为 1（一次更新，不发两次请求）", async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const params = requestParams(input);
    return pagedResponse(records, { total: 121, page: Number(params.get("page") ?? "1") });
  });
  vi.stubGlobal("fetch", fetchMock);
  const { onFilterChange } = renderControlled({ page: 3 });

  await waitFor(() => expect(fetchMock).toHaveBeenCalled());
  await userEvent.click(screen.getByText(/^视频 \(/));

  const kinds = onFilterChange.mock.calls.map(([patch]) => patch);
  expect(kinds).toContainEqual({ kind: "videos", page: 1 });
});

it("页码超出总页数时收敛到最后一页", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => pagedResponse(records, { total: 61, totalPages: 2 })));
  const { onFilterChange } = renderControlled({ page: 5 });

  await waitFor(() => expect(onFilterChange).toHaveBeenCalled());
  expect(onFilterChange.mock.calls.some(([patch, options]) => patch.page === 2 && options?.replace === true)).toBe(true);
});

it("加载失败展示错误并可重试", async () => {
  let attempt = 0;
  const fetchMock = vi.fn(async () => {
    attempt += 1;
    if (attempt === 1) throw new Error("网络错误");
    return pagedResponse(records);
  });
  vi.stubGlobal("fetch", fetchMock);
  renderGallery();

  await waitFor(() => expect(screen.getByText("媒体库加载失败")).toBeTruthy());
  await userEvent.click(screen.getByRole("button", { name: "重试" }));
  await waitFor(() => expect(screen.getByText("shared.jpg")).toBeTruthy());
});

it("retries video posters when their source changes after a failed load", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  let previewUrl = "https://pbs.twimg.com/old.jpg";
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => pagedResponse([{ ...mediaRecords[0], previewUrl }])),
  );
  render(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <GalleryPage />
      </TooltipProvider>
    </QueryClientProvider>,
  );
  await waitFor(() => expect(screen.getByAltText("clip-a.mp4")).toBeTruthy());
  // 两个候选源都失败后不再渲染 <img>
  fireEvent.error(screen.getByAltText("clip-a.mp4"));
  fireEvent.error(screen.getByAltText("clip-a.mp4"));
  expect(screen.queryByAltText("clip-a.mp4")).toBeNull();

  // 服务端返回了新的 previewUrl → 重新拉取后 VideoPoster 以新 key 重挂载，源索引归零
  previewUrl = "https://pbs.twimg.com/new.jpg";
  await client.invalidateQueries({ queryKey: ["library-downloads"] });

  await waitFor(() =>
    expect(screen.getByAltText("clip-a.mp4").getAttribute("src")).toBe(
      "/api/library/downloads/11/preview",
    ),
  );
});

it("loads the selected server page without resetting it to page one", async () => {
  const fetch = vi.fn(async (input: RequestInfo | URL) => {
    const page = Number(requestParams(input).get("page"));
    return pagedResponse(
      [{ ...records[0], id: page, filePath: `/downloads/server-page-${page}.jpg` }],
      { page, total: 121, totalPages: 3 },
    );
  });
  vi.stubGlobal("fetch", fetch);
  const { onFilterChange } = renderControlled();

  await waitFor(() => expect(screen.getByText("server-page-1.jpg")).toBeTruthy());
  await userEvent.click(screen.getByRole("button", { name: "第 2 页" }));

  expect(onFilterChange.mock.calls.map(([patch]) => patch)).toContainEqual({ page: 2 });
  await waitFor(() => expect(screen.getByText("server-page-2.jpg")).toBeTruthy());
  expect(screen.queryByText("server-page-1.jpg")).toBeNull();
  expect(screen.getByText("全部 (121)")).toBeTruthy();
});

function previewedFile() {
  return document.querySelector("[role=dialog] video, [role=dialog] img")?.getAttribute("src");
}

function pressArrow(target: EventTarget) {
  const event = new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true, cancelable: true });
  target.dispatchEvent(event);
  return event;
}

it("灯箱走到当前页末尾会翻页并续开下一页首个媒体", async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const page = Number(requestParams(input).get("page") ?? "1");
    const items =
      page === 1
        ? [mediaRecords[0]]
        : [{ ...mediaRecords[1], id: 22, filePath: "/downloads/clip-c.mp4" }];
    return pagedResponse(items, { page, total: 61, totalPages: 5 });
  });
  vi.stubGlobal("fetch", fetchMock);
  renderControlled();

  await waitFor(() => expect(screen.getByLabelText("预览视频 clip-a.mp4")).toBeTruthy());
  await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
  expect(previewedFile()).toBe("/api/library/downloads/11/file");

  await userEvent.click(screen.getByRole("button", { name: "下一个媒体文件" }));

  await waitFor(() => expect(previewedFile()).toBe("/api/library/downloads/22/file"));
  expect(fetchMock.mock.calls.some(([input]) => requestParams(input).get("page") === "2")).toBe(true);
});

describe("lightbox arrow keys", () => {
  it("steps to the next item when nothing interactive is focused", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => pagedResponse(mediaRecords)));
    renderGallery();
    await waitFor(() => expect(screen.getByLabelText("预览视频 clip-a.mp4")).toBeTruthy());
    await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
    expect(previewedFile()).toBe("/api/library/downloads/11/file");

    const event = pressArrow(document.body);

    expect(event.defaultPrevented).toBe(true);
    await waitFor(() => expect(previewedFile()).toBe("/api/library/downloads/12/file"));
  });

  it("leaves arrow keys to a focused video so it can seek", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => pagedResponse(mediaRecords)));
    renderGallery();
    await waitFor(() => expect(screen.getByLabelText("预览视频 clip-a.mp4")).toBeTruthy());
    await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
    const video = document.querySelector("[role=dialog] video");
    expect(video).not.toBeNull();

    const event = pressArrow(video!);

    expect(event.defaultPrevented).toBe(false);
    expect(previewedFile()).toBe("/api/library/downloads/11/file");
  });

  it("ignores arrow keys typed into text fields", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => pagedResponse(mediaRecords)));
    renderGallery();
    await waitFor(() => expect(screen.getByLabelText("预览视频 clip-a.mp4")).toBeTruthy());
    await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
    const input = document.createElement("input");
    document.body.appendChild(input);

    const event = pressArrow(input);

    expect(event.defaultPrevented).toBe(false);
    expect(previewedFile()).toBe("/api/library/downloads/11/file");
    input.remove();
  });
});

it("ignores arrow keys inside contenteditable regions", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => pagedResponse(mediaRecords)));
  renderGallery();
  await waitFor(() => expect(screen.getByLabelText("预览视频 clip-a.mp4")).toBeTruthy());
  await userEvent.click(screen.getByLabelText("预览视频 clip-a.mp4"));
  const editor = document.createElement("div");
  editor.setAttribute("contenteditable", "true");
  const inner = document.createElement("span");
  editor.appendChild(inner);
  document.body.appendChild(editor);

  const event = pressArrow(inner);

  expect(event.defaultPrevented).toBe(false);
  expect(previewedFile()).toBe("/api/library/downloads/11/file");
  editor.remove();
});
