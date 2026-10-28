// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import App from "./App";
import { TooltipProvider } from "./components/ui/Overlay";

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

function protectedResponse(path: string) {
  if (path === "/api/config") return json({ downloadDir: "/downloads", maxConcurrency: 1, proxyUrl: "", storageType: "local" });
  if (path.startsWith("/api/local-directories")) return json({ path: "/downloads", parent: "", entries: [] });
  return json({});
}

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><TooltipProvider><App /></TooltipProvider></QueryClientProvider>);
}

beforeEach(() => {
  localStorage.clear();
  window.history.replaceState(null, "", "/settings");
  vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener() {}, removeEventListener() {} }));
});

afterEach(() => { vi.unstubAllGlobals(); });

it("requires initial account setup before loading protected data", async () => {
  const fetchMock = vi.fn(async (path: string) => path === "/api/auth/session"
    ? json({ configured: false, authenticated: false }) : protectedResponse(path));
  vi.stubGlobal("fetch", fetchMock);
  renderApp();
  expect(await screen.findByRole("heading", { name: "创建账号" })).toBeTruthy();
  expect(screen.getByLabelText("用户名")).toBeTruthy();
  expect(screen.getByLabelText("确认密码")).toBeTruthy();
  expect(fetchMock.mock.calls.every(([path]) => path === "/api/auth/session")).toBe(true);
});

it("checks password confirmation before creating the single account", async () => {
  const fetchMock = vi.fn(async (path: string) => path === "/api/auth/session"
    ? json({ configured: false, authenticated: false }) : protectedResponse(path));
  vi.stubGlobal("fetch", fetchMock);
  renderApp();
  await screen.findByRole("heading", { name: "创建账号" });
  await userEvent.type(screen.getByLabelText("用户名"), "owner");
  await userEvent.type(screen.getByLabelText("密码"), "initial-password");
  await userEvent.type(screen.getByLabelText("确认密码"), "different-password");
  await userEvent.click(screen.getByRole("button", { name: "创建账号" }));
  expect(await screen.findByText("两次输入的密码不一致")).toBeTruthy();
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

it("keeps login across reloads and clears it after a password change", async () => {
  let token = "";
  const fetchMock = vi.fn(async (path: string, init?: RequestInit) => {
    if (path === "/api/auth/session") return json({ configured: true, authenticated: token !== "", username: "owner" });
    if (path === "/api/auth/login") { token = "permanent-token"; return json({ username: "owner", token }); }
    if (path === "/api/auth/password") { token = ""; return json({ ok: true }); }
    if (path === "/api/auth/check") return json({ ok: false });
    return protectedResponse(path);
  });
  vi.stubGlobal("fetch", fetchMock);
  const first = renderApp();
  await screen.findByRole("heading", { name: "登录" });
  await userEvent.type(screen.getByLabelText("用户名"), "owner");
  await userEvent.type(screen.getByLabelText("密码"), "initial-password");
  await userEvent.click(screen.getByRole("button", { name: "登录" }));
  await screen.findByRole("heading", { name: "账号安全" });
  expect(Object.values(localStorage)).toContain("permanent-token");
  first.unmount();
  renderApp();
  await screen.findByRole("heading", { name: "账号安全" });
  const lastStatus = fetchMock.mock.calls.filter(([path]) => path === "/api/auth/session").at(-1);
  expect(new Headers(lastStatus?.[1]?.headers).get("Authorization")).toBe("Bearer permanent-token");
  await userEvent.type(screen.getByLabelText("当前密码"), "initial-password");
  await userEvent.type(screen.getByLabelText("新密码"), "replacement-password");
  await userEvent.type(screen.getByLabelText("确认新密码"), "replacement-password");
  await userEvent.click(screen.getByRole("button", { name: "修改密码" }));
  expect(await screen.findByRole("heading", { name: "登录" })).toBeTruthy();
  expect(Object.values(localStorage)).not.toContain("permanent-token");
  const passwordRequest = fetchMock.mock.calls.find(([path]) => path === "/api/auth/password");
  expect(JSON.parse(String(passwordRequest?.[1]?.body))).toEqual({ currentPassword: "initial-password", newPassword: "replacement-password" });
});

it("returns to login when a previously valid token is rejected", async () => {
  vi.stubGlobal("fetch", vi.fn(async (path: string) => path === "/api/auth/session"
    ? json({ configured: true, authenticated: true, username: "owner" })
    : json({ error: "请先登录" }, 401)));
  renderApp();
  await waitFor(() => expect(screen.getByRole("heading", { name: "登录" })).toBeTruthy());
});

it("ignores a session check that finishes after a newer login", async () => {
  let sessionCalls = 0;
  let finishCheck!: (response: Response) => void;
  vi.stubGlobal("fetch", vi.fn(async (path: string) => {
    if (path === "/api/auth/session") {
      if (++sessionCalls > 1) return new Promise<Response>((resolve) => { finishCheck = resolve; });
      return json({ configured: true, authenticated: false });
    }
    if (path === "/api/auth/login") return json({ username: "owner", token: "newer-token" });
    return protectedResponse(path);
  }));
  renderApp();
  await screen.findByRole("heading", { name: "登录" });
  act(() => window.dispatchEvent(new Event("focus")));
  await userEvent.type(screen.getByLabelText("用户名"), "owner");
  await userEvent.type(screen.getByLabelText("密码"), "initial-password");
  await userEvent.click(screen.getByRole("button", { name: "登录" }));
  await screen.findByRole("heading", { name: "账号安全" });
  await act(async () => { finishCheck(json({ configured: true, authenticated: false })); });
  expect(screen.getByRole("heading", { name: "账号安全" })).toBeTruthy();
  expect(Object.values(localStorage)).toContain("newer-token");
});
