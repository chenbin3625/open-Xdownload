// @vitest-environment jsdom
import { QueryClient } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  archiveScheduleQueryRoot,
  dashboardMetaQueryRoot,
  failedTweetQueryRoot,
  jobsQueryRoot,
  libraryDownloadsQueryRoot,
} from "./api";
import { useDashboardEvents } from "./useDashboardEvents";

// jsdom 没有 EventSource，这里用最小桩手动驱动 open / error。
class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];

  readyState = FakeEventSource.CONNECTING;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((message: { data: string }) => void) | null = null;

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  close() {
    this.readyState = FakeEventSource.CLOSED;
  }

  open() {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }

  fail({ closed = false } = {}) {
    this.readyState = closed ? FakeEventSource.CLOSED : FakeEventSource.CONNECTING;
    this.onerror?.();
  }
}

const roots = [
  jobsQueryRoot,
  dashboardMetaQueryRoot,
  libraryDownloadsQueryRoot,
  archiveScheduleQueryRoot,
  failedTweetQueryRoot,
] as const;

function seededClient() {
  const queryClient = new QueryClient();
  for (const root of roots) queryClient.setQueryData(root, {});
  return queryClient;
}

function invalidatedRoots(queryClient: QueryClient) {
  return roots.filter((root) => queryClient.getQueryState(root)?.isInvalidated);
}

function latestSource() {
  return FakeEventSource.instances[FakeEventSource.instances.length - 1];
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.useFakeTimers();
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("useDashboardEvents reconnect", () => {
  it("does not refetch on the initial connection", () => {
    const queryClient = seededClient();
    renderHook(() => useDashboardEvents(queryClient, vi.fn()));

    act(() => latestSource().open());

    expect(invalidatedRoots(queryClient)).toEqual([]);
  });

  it("refreshes events missed while the subscription was disabled", () => {
    const queryClient = seededClient();
    const onRefresh = vi.fn();
    const { rerender } = renderHook(
      ({ enabled }) => useDashboardEvents(queryClient, onRefresh, enabled),
      { initialProps: { enabled: true } },
    );
    const first = latestSource();
    act(() => first.open());

    rerender({ enabled: false });
    expect(first.readyState).toBe(FakeEventSource.CLOSED);
    expect(invalidatedRoots(queryClient)).toEqual([]);

    rerender({ enabled: true });
    act(() => latestSource().open());

    expect(invalidatedRoots(queryClient)).toEqual([...roots]);
  });

  it("refetches workbench, schedule and failed-tweet queries after reconnecting", () => {
    const queryClient = seededClient();
    const { result } = renderHook(() => useDashboardEvents(queryClient, vi.fn()));
    const source = latestSource();

    act(() => source.open());
    act(() => source.fail());
    expect(result.current.sseConnected).toBe(false);

    act(() => source.open());

    expect(result.current.sseConnected).toBe(true);
    expect(invalidatedRoots(queryClient)).toEqual([...roots]);
  });

  it("still refetches when the reconnect happens on a rebuilt EventSource", () => {
    const queryClient = seededClient();
    renderHook(() => useDashboardEvents(queryClient, vi.fn()));

    act(() => latestSource().open());
    act(() => latestSource().fail({ closed: true }));
    act(() => {
      vi.advanceTimersByTime(3_000);
    });
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(invalidatedRoots(queryClient)).toEqual([]);

    act(() => latestSource().open());

    expect(invalidatedRoots(queryClient)).toEqual([...roots]);
  });
});

describe("useDashboardEvents 手动重连", () => {
  it("重建 EventSource，并在重连成功后整体补拉", () => {
    const queryClient = seededClient();
    const { result } = renderHook(() => useDashboardEvents(queryClient, vi.fn()));
    const first = latestSource();

    act(() => first.open());
    expect(result.current.sseConnected).toBe(true);

    act(() => result.current.reconnect());

    // 旧实例被关闭、新实例已创建，状态先回到"断开"
    expect(first.readyState).toBe(FakeEventSource.CLOSED);
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(result.current.sseConnected).toBe(false);

    act(() => latestSource().open());
    expect(result.current.sseConnected).toBe(true);
    expect(invalidatedRoots(queryClient)).toEqual([...roots]);
  });
});

describe("useDashboardEvents buffered job.updated ordering", () => {
  it("discards buffered update for a deleted job and flushes pending updates before job.created", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const jobsKey = [...jobsQueryRoot, 1, 10, "all", "all", ""];
    const job1 = {
      id: 1, kind: "tweet_link" as const, status: "downloading" as const, input: "u1", title: "Job 1",
      progress: 0.1, message: "", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:01Z",
    };
    const job2 = {
      id: 2, kind: "tweet_link" as const, status: "downloading" as const, input: "u2", title: "Job 2",
      progress: 0.1, message: "", createdAt: "2026-01-01T00:00:02Z", updatedAt: "2026-01-01T00:00:02Z",
    };
    queryClient.setQueryData(jobsKey, {
      items: [job2, job1], page: 1, pageSize: 10, total: 2,
      counts: { all: 2, active: 2, completed: 0, failed: 0 },
    });
    queryClient.setQueryData(dashboardMetaQueryRoot, {
      stats: { total: 2, active: 2, completed: 0, failed: 0 },
      failedTweetCount: 0,
    });

    renderHook(() => useDashboardEvents(queryClient, vi.fn()));
    const source = latestSource();
    act(() => source.open());

    // Queue a non-terminal job.updated for job1 carrying stale meta (total=2)
    act(() => {
      source.onmessage?.({
        data: JSON.stringify({
          type: "job.updated",
          payload: { ...job1, progress: 0.6, updatedAt: "2026-01-01T00:00:05Z" },
          meta: { stats: { total: 2, active: 2, completed: 0, failed: 0 }, failedTweetCount: 0 },
        }),
      });
    });

    // Before 250ms elapses, job1 is deleted with newer meta (total=1)
    act(() => {
      source.onmessage?.({
        data: JSON.stringify({
          type: "job.deleted",
          jobId: 1,
          meta: { stats: { total: 1, active: 1, completed: 0, failed: 0 }, failedTweetCount: 0 },
        }),
      });
    });

    // Advance past 250ms debounce: stale buffered job.updated must NOT overwrite total=1
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(queryClient.getQueryData<{ stats: { total: number; active: number } }>(dashboardMetaQueryRoot)?.stats).toMatchObject({
      total: 1,
      active: 1,
    });

    // Now queue a non-terminal update for job2 (stale meta total=1), then immediately receive job.created (total=2)
    act(() => {
      source.onmessage?.({
        data: JSON.stringify({
          type: "job.updated",
          payload: { ...job2, progress: 0.8, updatedAt: "2026-01-01T00:00:10Z" },
          meta: { stats: { total: 1, active: 1, completed: 0, failed: 0 }, failedTweetCount: 0 },
        }),
      });
      source.onmessage?.({
        data: JSON.stringify({
          type: "job.created",
          payload: {
            id: 3, kind: "tweet_link", status: "pending", input: "u3", title: "Job 3",
            progress: 0, message: "", createdAt: "2026-01-01T00:00:11Z", updatedAt: "2026-01-01T00:00:11Z",
          },
          meta: { stats: { total: 2, active: 2, completed: 0, failed: 0 }, failedTweetCount: 0 },
        }),
      });
    });

    // Job2 update was flushed synchronously before job.created, and job.created meta wins even after 500ms
    act(() => {
      vi.advanceTimersByTime(500);
    });
    const page = queryClient.getQueryData<{ items: Array<{ id: number; progress: number }> }>(jobsKey);
    expect(page?.items.find((item) => item.id === 2)?.progress).toBe(0.8);
    expect(page?.items.some((item) => item.id === 3)).toBe(true);
    expect(queryClient.getQueryData<{ stats: { total: number; active: number } }>(dashboardMetaQueryRoot)?.stats).toMatchObject({
      total: 2,
      active: 2,
    });
  });
});
