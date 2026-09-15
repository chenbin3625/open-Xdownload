// @vitest-environment jsdom
import { renderWithProviders as render, screen } from "../../test/render";
import userEvent from "@testing-library/user-event";
import React from "react";
import { describe, expect, it, vi } from "vitest";
import { AppSidebar, type AppSidebarProps } from "./AppSidebar";

const baseProps: AppSidebarProps = {
  activeSection: "tasks",
  onSectionChange: () => {},
  onOpenCreateModal: () => {},
  onOpenFailedDrawer: () => {},
  failedTweetCount: 0,
};

describe("AppSidebar", () => {
  it("品牌区不展示版本号", () => {
    render(<AppSidebar {...baseProps} />);

    expect(screen.getByText("open-Xdownload")).not.toBeNull();
    expect(screen.queryByText(`v${__APP_VERSION__}`)).toBeNull();
  });

  it("用 aria-current 标记当前导航项", () => {
    render(<AppSidebar {...baseProps} activeSection="gallery" />);

    expect(screen.getByRole("button", { name: /媒体归档库/ }).getAttribute("aria-current")).toBe(
      "page",
    );
    expect(
      screen.getByRole("button", { name: /任务调度中心/ }).hasAttribute("aria-current"),
    ).toBe(false);
  });

  it("overview / workbench / tasks 都落在任务调度中心一项上", () => {
    for (const section of ["overview", "workbench", "tasks"] as const) {
      const { unmount } = render(<AppSidebar {...baseProps} activeSection={section} />);
      expect(
        screen.getByRole("button", { name: /任务调度中心/ }).getAttribute("aria-current"),
      ).toBe("page");
      unmount();
    }
  });

  it("点击导航项回传 section key", async () => {
    const user = userEvent.setup();
    const onSectionChange = vi.fn();
    render(<AppSidebar {...baseProps} onSectionChange={onSectionChange} />);

    await user.click(screen.getByRole("button", { name: /系统与存储配置/ }));
    expect(onSectionChange).toHaveBeenCalledWith("settings");
  });

  it("菜单项只展示名称，不展示数量徽标", () => {
    render(<AppSidebar {...baseProps} failedTweetCount={5} />);

    expect(screen.getByRole("button", { name: "任务调度中心" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "自动归档计划" })).not.toBeNull();
    expect(screen.queryByText("3 运行")).toBeNull();
    expect(screen.queryByText("12")).toBeNull();
    expect(screen.queryByText("7")).toBeNull();
    expect(screen.queryByText("5")).toBeNull();
  });

  it("没有失败推文时不渲染失败队列入口", () => {
    const { unmount } = render(<AppSidebar {...baseProps} failedTweetCount={0} />);
    expect(screen.queryByText("失败推文队列")).toBeNull();
    unmount();

    render(<AppSidebar {...baseProps} failedTweetCount={5} />);
    expect(screen.getByText("失败推文队列")).not.toBeNull();
    expect(screen.getByRole("button", { name: "查看并批量重试" })).not.toBeNull();
  });
});
