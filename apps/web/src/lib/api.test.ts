import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, formatBytes } from "./api";

describe("formatBytes", () => {
  it("formats zero", () => {
    expect(formatBytes(0)).toBe("0 B");
  });

  it("formats bytes without decimals", () => {
    expect(formatBytes(512)).toBe("512 B");
  });

  it("formats KB/MB/GB", () => {
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(10 * 1024 * 1024)).toBe("10.0 MB");
    expect(formatBytes(2.5 * 1024 * 1024 * 1024)).toBe("2.5 GB");
  });

  it("handles undefined-ish falsy input", () => {
    expect(formatBytes(undefined as unknown as number)).toBe("0 B");
  });
});

describe("api error handling", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function stubResponse(body: string, init: ResponseInit) {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(body, init)));
  }

  async function caught(promise: Promise<unknown>) {
    try {
      await promise;
    } catch (error) {
      return error;
    }
    throw new Error("expected the request to reject");
  }

  it("uses the server error message and keeps the status", async () => {
    stubResponse(JSON.stringify({ error: "任务不存在" }), { status: 404, statusText: "Not Found" });
    const error = await caught(api("/api/jobs/1"));
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toBeInstanceOf(Error);
    expect((error as ApiError).message).toBe("任务不存在");
    expect((error as ApiError).status).toBe(404);
  });

  it.each([
    ["an empty error field", JSON.stringify({ error: "" })],
    ["an empty object", "{}"],
    ["a non-JSON body", "upstream timeout"],
  ])("falls back to the status for %s", async (_label, body) => {
    // HTTP/2 响应没有 reason phrase，statusText 为空串
    stubResponse(body, { status: 502, statusText: "" });
    const error = (await caught(api("/api/jobs"))) as ApiError;
    expect(error.message).toBe("请求失败 (502)");
    expect(error.status).toBe(502);
  });

  it("prefers statusText over the generic fallback when present", async () => {
    stubResponse("{}", { status: 503, statusText: "Service Unavailable" });
    const error = (await caught(api("/api/jobs"))) as ApiError;
    expect(error.message).toBe("Service Unavailable");
  });
});
