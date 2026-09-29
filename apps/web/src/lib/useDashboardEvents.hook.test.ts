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
