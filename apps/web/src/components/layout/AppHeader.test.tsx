// @vitest-environment jsdom
import { renderWithProviders as render, screen } from "../../test/render";
import userEvent from "@testing-library/user-event";
import React from "react";
import { describe, expect, it, vi } from "vitest";
import { AppHeader } from "./AppHeader";
import { TooltipProvider } from "../semi";

const baseProps = {
  refreshPending: false,
  onRefresh: () => {},
  onQuickSubmit: () => {},
  failedTweetCount: 0,
  onOpenFailedDrawer: () => {},
};

describe("AppHeader", () => {
  it("再次点击铃铛关闭通知中心", async () => {
    const user = userEvent.setup();
    render(<AppHeader {...baseProps} />);
    const bell = screen.getByRole("button", { name: "通知中心" });
    await user.click(bell);
    expect(await screen.findByText("暂无通知")).not.toBeNull();
    await user.click(bell);
    expect(bell.getAttribute("aria-expanded")).toBe("false");
  });

  it("通过通知中心展示失败数量并打开处理抽屉", async () => {
    const user = userEvent.setup();
    const onOpenFailedDrawer = vi.fn();
    render(<AppHeader {...baseProps} failedTweetCount={5} onOpenFailedDrawer={onOpenFailedDrawer} />);

    expect(screen.getByRole("button", { name: "通知中心" }).textContent).toBe("5");
    expect(screen.queryByText("失败队列")).toBeNull();
    await user.click(screen.getByRole("button", { name: "通知中心" }));
    expect(await screen.findByText("失败队列")).not.toBeNull();
    expect(screen.getByText("5 条待处理")).not.toBeNull();
    await user.click(screen.getByRole("button", { name: "处理失败" }));
    expect(onOpenFailedDrawer).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "通知中心" }).getAttribute("aria-expanded")).toBe("false");
  });

  it("失败数量清零后通知中心显示空状态", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<AppHeader {...baseProps} failedTweetCount={5} />);
    await user.click(screen.getByRole("button", { name: "通知中心" }));
    expect(await screen.findByText("5 条待处理")).not.toBeNull();

    rerender(<TooltipProvider><AppHeader {...baseProps} failedTweetCount={0} /></TooltipProvider>);
    expect(await screen.findByText("暂无通知")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "处理失败" })).toBeNull();
    expect(screen.getByRole("button", { name: "通知中心" }).textContent).toBe("");
  });

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

  it("右上角展示版本并移除运行状态标签", () => {
    render(<AppHeader {...baseProps} />);

    expect(screen.getByText(`v${__APP_VERSION__}`)).not.toBeNull();
    expect(screen.queryByText("实时连接")).toBeNull();
    expect(screen.queryByText("正在重连")).toBeNull();
    expect(screen.queryByText(/运行中/)).toBeNull();
  });

  it("不再提供主题或强调色切换入口", () => {
    render(<AppHeader {...baseProps} />);

    expect(screen.queryByRole("button", { name: "切换主题" })).toBeNull();
    expect(screen.queryByRole("button", { name: "选择主题强调色" })).toBeNull();
  });

  it("SSE 断开时显示提示且点击可重连", async () => {
    const user = userEvent.setup();
    const onReconnectRealtime = vi.fn();
    render(
      <AppHeader
        {...baseProps}
        realtimeDisconnected
        onReconnectRealtime={onReconnectRealtime}
      />,
    );

    const banner = screen.getByTitle("实时更新已断开，点击重连");
    expect(banner).not.toBeNull();
    await user.click(banner);
    expect(onReconnectRealtime).toHaveBeenCalledTimes(1);
  });

  it("连接正常时不渲染断线提示", () => {
    render(<AppHeader {...baseProps} />);
    expect(screen.queryByTitle("实时更新已断开，点击重连")).toBeNull();
  });

  it("仅在紧凑布局下渲染菜单按钮", () => {
    const { unmount } = render(<AppHeader {...baseProps} showMenuButton={false} />);
    expect(screen.queryByRole("button", { name: "打开导航菜单" })).toBeNull();
    unmount();

    render(<AppHeader {...baseProps} showMenuButton />);
    expect(screen.getByRole("button", { name: "打开导航菜单" })).not.toBeNull();
  });
});
