// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import React from "react";
import { beforeEach, expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import { act, renderWithProviders, screen, waitFor } from "../../test/render";
import { AccountSecurity } from "../settings/AccountSecurity";
import { AuthGate } from "./AuthGate";

vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  getLoginSession: vi.fn(),
  loginAccount: vi.fn(),
  logoutAccount: vi.fn(),
}));

beforeEach(() => {
  vi.clearAllMocks();
});

it("does not unmount LoginForm on transient network errors during focus or storage checks", async () => {
  vi.mocked(api.getLoginSession)
    .mockResolvedValueOnce({ configured: true, authenticated: false })
    .mockRejectedValueOnce(new Error("network blip"));

  const user = userEvent.setup();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(
    <QueryClientProvider client={client}>
      <AuthGate><div>app</div></AuthGate>
    </QueryClientProvider>,
  );

  const usernameInput = (await screen.findByRole("textbox", { name: "用户名" })) as HTMLInputElement;
  await user.type(usernameInput, "alice");

  await act(async () => {
    window.dispatchEvent(new Event("focus"));
  });
  expect(api.getLoginSession).toHaveBeenCalledTimes(1);
  expect(usernameInput.value).toBe("alice");

  await act(async () => {
    window.dispatchEvent(new StorageEvent("storage", { key: "open-xdownload-session-changed" }));
  });
  await waitFor(() => expect(api.getLoginSession).toHaveBeenCalledTimes(2));
  expect((screen.getByRole("textbox", { name: "用户名" }) as HTMLInputElement).value).toBe("alice");
  expect(screen.queryByRole("alert")).toBeNull();
});

it("does not show session-expired notice on intentional logout", async () => {
  vi.mocked(api.getLoginSession).mockResolvedValue({ configured: true, authenticated: true, username: "alice" });
  vi.mocked(api.logoutAccount).mockResolvedValue({ ok: true });

  const user = userEvent.setup();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(
    <QueryClientProvider client={client}>
      <AuthGate><AccountSecurity /></AuthGate>
    </QueryClientProvider>,
  );

  await user.click(await screen.findByRole("button", { name: "退出登录" }));
  expect(await screen.findByRole("heading", { name: "登录" })).toBeTruthy();
  expect(screen.queryByText("登录已失效，请重新登录")).toBeNull();
});

it("trims whitespace around username and drops minLength after 409 conflict switches to login mode", async () => {
  vi.mocked(api.getLoginSession).mockResolvedValue({ configured: false, authenticated: false });
  vi.mocked(api.loginAccount)
    .mockRejectedValueOnce(new api.ApiError("账号已存在", 409))
    .mockResolvedValueOnce({ username: "alice" });

  const user = userEvent.setup();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(
    <QueryClientProvider client={client}>
      <AuthGate><div>app-ready</div></AuthGate>
    </QueryClientProvider>,
  );

  const usernameInput = (await screen.findByRole("textbox", { name: "用户名" })) as HTMLInputElement;
  const passwordInput = screen.getByLabelText("密码") as HTMLInputElement;
  const confirmInput = screen.getByLabelText("确认密码") as HTMLInputElement;
  expect(passwordInput.getAttribute("minlength")).toBe("8");

  await user.type(usernameInput, "  alice  ");
  await user.type(passwordInput, "password123");
  await user.type(confirmInput, "password123");
  await user.click(screen.getByRole("button", { name: "创建账号" }));

  // 409 switches from setup mode back to login mode and removes registration minLength=8
  expect(await screen.findByRole("heading", { name: "登录" })).toBeTruthy();
  expect(api.loginAccount).toHaveBeenNthCalledWith(1, "alice", "password123", true);
  const loginPassword = screen.getByLabelText("密码") as HTMLInputElement;
  expect(loginPassword.getAttribute("minlength")).toBeNull();

  await user.clear(loginPassword);
  await user.type(loginPassword, "short");
  await user.click(screen.getByRole("button", { name: "登录" }));

  await waitFor(() => expect(api.loginAccount).toHaveBeenNthCalledWith(2, "alice", "short", false));
  expect(await screen.findByText("app-ready")).toBeTruthy();
});
