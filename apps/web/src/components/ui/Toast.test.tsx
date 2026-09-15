// @vitest-environment jsdom
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Toaster, toast } from "./Toast";

// 命令式 API 的 store 是模块级的，用例之间必须清干净
function clearAll() {
  for (const item of document.querySelectorAll("[aria-label='关闭提示']")) {
    (item as HTMLElement).click();
  }
}

afterEach(() => {
  act(() => clearAll());
  vi.useRealTimers();
});

describe("toast", () => {
  it("在 React 树之外命令式调用即可渲染", () => {
    render(<Toaster />);

    act(() => {
      toast.success({ message: "任务已取消" });
    });

    expect(screen.getByText("任务已取消")).not.toBeNull();
  });

  it("同时存在多条，按调用顺序排列", () => {
    render(<Toaster />);

    act(() => {
      toast.info({ message: "第一条" });
      toast.error({ message: "第二条", description: "失败原因" });
    });

    expect(screen.getByText("第一条")).not.toBeNull();
    expect(screen.getByText("第二条")).not.toBeNull();
    expect(screen.getByText("失败原因")).not.toBeNull();
  });

  it("点击关闭按钮移除对应那一条", async () => {
    const user = userEvent.setup();
    render(<Toaster />);

    act(() => {
      toast.info({ message: "留下的" });
      toast.info({ message: "要关的" });
    });

    await user.click(screen.getAllByRole("button", { name: "关闭提示" })[1]);
    expect(screen.queryByText("要关的")).toBeNull();
    expect(screen.getByText("留下的")).not.toBeNull();
  });

  it("duration 到点后自动消失", () => {
    vi.useFakeTimers();
    render(<Toaster />);

    act(() => {
      toast.info({ message: "会自动消失", duration: 1000 });
    });
    expect(screen.getByText("会自动消失")).not.toBeNull();

    act(() => {
      vi.advanceTimersByTime(1100);
    });
    expect(screen.queryByText("会自动消失")).toBeNull();
  });

  it("dismiss(id) 可主动关闭指定通知", () => {
    render(<Toaster />);

    let id = 0;
    act(() => {
      id = toast.warning({ message: "手动关闭" });
    });
    expect(screen.getByText("手动关闭")).not.toBeNull();

    act(() => {
      toast.dismiss(id);
    });
    expect(screen.queryByText("手动关闭")).toBeNull();
  });

  it("容器常驻 aria-live，内容变化才播报", () => {
    render(<Toaster />);
    // 容器随通知一起挂载/卸载会导致读屏漏读，因此空列表时也要在
    const live = document.querySelector("[aria-live='polite']");
    expect(live).not.toBeNull();
  });
});
