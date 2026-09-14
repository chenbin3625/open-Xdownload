// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React, { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Button } from "./Button";
import { Drawer, Modal, Popconfirm } from "./Overlay";

describe("Modal", () => {
  it("焦点被限制在弹窗内，Tab 不会跑到背景元素上", async () => {
    const user = userEvent.setup();
    render(
      <>
        <button type="button">背景按钮</button>
        <Modal open onClose={() => {}} title="标题" footer={<Button>确定</Button>}>
          <input aria-label="弹窗输入" />
        </Modal>
      </>,
    );

    const dialog = screen.getByRole("dialog");
    // 连续 Tab 一圈，焦点始终落在 dialog 子树内
    for (let step = 0; step < 6; step += 1) {
      await user.tab();
      expect(dialog.contains(document.activeElement)).toBe(true);
    }
  });

  it("ESC 触发 onClose", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(
      <Modal open onClose={onClose} title="标题">
        内容
      </Modal>,
    );

    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("关闭时不渲染任何内容", () => {
    render(
      <Modal open={false} onClose={() => {}} title="标题">
        内容
      </Modal>,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("Drawer", () => {
  it("无可见标题时提供 sr-only 标题供读屏定位", () => {
    render(
      <Drawer open onClose={() => {}}>
        菜单
      </Drawer>,
    );
    // Radix 要求 Dialog.Content 内必须有 Title，否则控制台告警
    expect(screen.getByRole("dialog").getAttribute("aria-labelledby")).not.toBeNull();
  });
});

describe("Popconfirm", () => {
  it("确认后回调一次并关闭气泡", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <Popconfirm title="确认删除？" onConfirm={onConfirm}>
        <Button>删除</Button>
      </Popconfirm>,
    );

    await user.click(screen.getByRole("button", { name: "删除" }));
    await user.click(screen.getByRole("button", { name: "确定" }));

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("确认删除？")).toBeNull();
  });

  it("disabled 时直接透传子元素，不挂弹出行为", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <Popconfirm title="确认删除？" disabled onConfirm={onConfirm}>
        <Button>删除</Button>
      </Popconfirm>,
    );

    await user.click(screen.getByRole("button", { name: "删除" }));
    expect(screen.queryByText("确认删除？")).toBeNull();
    expect(onConfirm).not.toHaveBeenCalled();
  });
});

describe("Modal 受控关闭", () => {
  it("点击 footer 按钮后由父组件收起", async () => {
    const user = userEvent.setup();
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <Modal
          open={open}
          onClose={() => setOpen(false)}
          title="标题"
          footer={<Button onClick={() => setOpen(false)}>关闭弹窗</Button>}
        >
          内容
        </Modal>
      );
    }
    render(<Harness />);

    await user.click(screen.getByRole("button", { name: "关闭弹窗" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
