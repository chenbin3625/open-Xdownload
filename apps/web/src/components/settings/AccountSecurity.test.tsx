// @vitest-environment jsdom
import userEvent from "@testing-library/user-event";
import React from "react";
import { beforeEach, expect, it, vi } from "vitest";
import * as api from "../../lib/api";
import { act, renderWithProviders, screen, waitFor } from "../../test/render";
import { AccountContext } from "../auth/AuthGate";
import { AccountSecurity } from "./AccountSecurity";

vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  logoutAccount: vi.fn(),
  changeAccountPassword: vi.fn(),
}));

beforeEach(() => {
  vi.clearAllMocks();
});

it("keeps logout errors separate from the change-password modal and disables change password while logout is pending", async () => {
  let rejectLogout!: (err: Error) => void;
  vi.mocked(api.logoutAccount).mockImplementation(
    () => new Promise((_resolve, reject) => { rejectLogout = reject; }),
  );
  const user = userEvent.setup();
  renderWithProviders(
    <AccountContext.Provider value="admin">
      <AccountSecurity />
    </AccountContext.Provider>,
  );

  const changeBtn = screen.getByRole("button", { name: "修改密码" });
  const logoutBtn = screen.getByRole("button", { name: "退出登录" });

  await user.click(logoutBtn);
  expect(changeBtn.hasAttribute("disabled")).toBe(true);
  expect(logoutBtn.hasAttribute("disabled")).toBe(true);

  await act(async () => rejectLogout(new Error("logout failed")));
  await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("logout failed"));
  expect(changeBtn.hasAttribute("disabled")).toBe(false);

  // Opening change-password modal should NOT show the logout error inside the modal
  await user.click(changeBtn);
  const dialog = screen.getByRole("dialog");
  expect(dialog.querySelector("[role=\"alert\"]")).toBeNull();
});
