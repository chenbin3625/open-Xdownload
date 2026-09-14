// @vitest-environment jsdom
import { renderWithProviders as render, screen } from "../../test/render";
import userEvent from "@testing-library/user-event";
import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AppHeader } from "./AppHeader";

const baseProps = {
  sseConnected: true,
  activeCount: 0,
  refreshPending: false,
  onRefresh: () => {},
  onQuickSubmit: () => {},
};

beforeEach(() => {
  document.documentElement.className = "";
  window.localStorage.clear();
  // useTheme 通过 matchMedia 读系统偏好，jsdom 未实现
  if (!window.matchMedia) {
    window.matchMedia = ((query: string) => ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    })) as unknown as typeof window.matchMedia;
  }
});

describe("AppHeader", () => {
  it("回车与点击解析都提交并清空输入", async () => {
    const user = userEvent.setup();
    const onQuickSubmit = vi.fn();
    render(<AppHeader {...baseProps} onQuickSubmit={onQuickSubmit} />);

    const input = screen.getByLabelText("快速解析输入");
    await user.type(input, "  @someone  {Enter}");
    // 提交前会 trim，前后空格不该带进任务输入
    expect(onQuickSubmit).toHaveBeenCalledWith("@someone");
    expect((input as HTMLInputElement).value).toBe("");

    await user.type(input, "https://x.com/a/status/1");
    await user.click(screen.getByRole("button", { name: "解析" }));
    expect(onQuickSubmit).toHaveBeenLastCalledWith("https://x.com/a/status/1");
  });

  it("空输入时解析按钮禁用，回车不触发提交", async () => {
    const user = userEvent.setup();
    const onQuickSubmit = vi.fn();
    render(<AppHeader {...baseProps} onQuickSubmit={onQuickSubmit} />);

    expect(screen.getByRole("button", { name: "解析" }).hasAttribute("disabled")).toBe(true);

    await user.type(screen.getByLabelText("快速解析输入"), "   {Enter}");
    expect(onQuickSubmit).not.toHaveBeenCalled();
  });

  it("按连接状态切换标签文案", () => {
    const { unmount } = render(<AppHeader {...baseProps} sseConnected />);
    expect(screen.getByText("实时连接")).not.toBeNull();
    unmount();

    render(<AppHeader {...baseProps} sseConnected={false} />);
    expect(screen.getByText("正在重连")).not.toBeNull();
  });

  it("主题按钮切换 html 上的类名", async () => {
    const user = userEvent.setup();
    render(<AppHeader {...baseProps} />);

    await user.click(screen.getByRole("button", { name: "切换主题" }));
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    await user.click(screen.getByRole("button", { name: "切换主题" }));
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    // 显式选择浅色时必须带上 .light，否则 CSS 里
    // html:not(.light):not(.dark) 的系统兜底会把它又拉回深色
    expect(document.documentElement.classList.contains("light")).toBe(true);
  });

  it("仅在紧凑布局下渲染菜单按钮", () => {
    const { unmount } = render(<AppHeader {...baseProps} showMenuButton={false} />);
    expect(screen.queryByRole("button", { name: "打开导航菜单" })).toBeNull();
    unmount();

    render(<AppHeader {...baseProps} showMenuButton />);
    expect(screen.getByRole("button", { name: "打开导航菜单" })).not.toBeNull();
  });
});
