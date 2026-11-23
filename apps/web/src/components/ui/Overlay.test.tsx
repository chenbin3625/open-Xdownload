// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React, { useEffect, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Button } from "@douyinfe/semi-ui";
import { Drawer, Modal, Popconfirm, SearchSelect, Select, SimplePopover } from "../semi";

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
    const onClose = vi.fn();
    render(
      <Modal open onClose={onClose} title="标题">
        内容
      </Modal>,
    );

    fireEvent.keyDown(document, { key: "Escape", keyCode: 27 });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it.each([["Select", Select], ["SearchSelect", SearchSelect]] as const)("%s 的 Escape 先关闭选项层，再关闭弹窗", async (_name, Control) => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    function Harness() {
      const [open, setOpen] = useState(true);
      return <Modal open={open} onClose={() => { onClose(); setOpen(false); }} title="选择存储">
        <Control ariaLabel="存储目标" value="local" onChange={() => {}} options={[{ value: "local", label: "本地" }]} />
      </Modal>;
    }
    render(<Harness />);
    const trigger = screen.getByRole("combobox", { name: "存储目标" });
    await user.click(trigger);
    expect(screen.getByRole("option", { name: "本地" })).not.toBeNull();
    fireEvent.keyDown(document.activeElement!, { key: "Escape", keyCode: 27 });
    await waitFor(() => expect(screen.queryByRole("option")).toBeNull());
    expect(onClose).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(trigger);
    fireEvent.keyDown(trigger, { key: "Escape", keyCode: 27 });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("Popconfirm 在 Modal 内按 Escape 只关闭气泡，再次按 Escape 才关闭 Modal", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <Modal open={open} onClose={() => { onClose(); setOpen(false); }} title="编辑任务">
          <Popconfirm title="确认删除？" onConfirm={() => {}}>
            <Button>删除</Button>
          </Popconfirm>
        </Modal>
      );
    }
    render(<Harness />);
    const trigger = screen.getByRole("button", { name: "删除" });
    await user.click(trigger);
    expect(screen.getByRole("alertdialog")).not.toBeNull();

    fireEvent.keyDown(screen.getByRole("button", { name: "取消" }), { key: "Escape", keyCode: 27 });
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).not.toBeNull();

    // Popconfirm 已关闭时，再按 Escape 应正常关闭外层 Modal
    fireEvent.keyDown(trigger, { key: "Escape", keyCode: 27 });
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

  it("焦点被限制在抽屉内，Tab / Shift+Tab 首尾循环且无聚焦元素时留在容器内", () => {
    const { rerender } = render(
      <>
        <button type="button">背景按钮</button>
        <Drawer open onClose={() => {}} title="抽屉">
          <button type="button">首按钮</button>
          <button type="button">尾按钮</button>
        </Drawer>
      </>,
    );

    const dialog = screen.getByRole("dialog");
    const first = screen.getByRole("button", { name: "首按钮" });
    const last = screen.getByRole("button", { name: "尾按钮" });
    // Semi SideSheet 自带关闭按钮，定位 dialog 内实际首尾可聚焦节点
    const focusables = Array.from(dialog.querySelectorAll<HTMLElement>("button:not([disabled])"));
    const firstEl = focusables[0]!;
    const lastEl = focusables[focusables.length - 1]!;
    expect(focusables).toContain(first);
    expect(focusables).toContain(last);

    lastEl.focus();
    fireEvent.keyDown(lastEl, { key: "Tab" });
    expect(document.activeElement).toBe(firstEl);

    firstEl.focus();
    fireEvent.keyDown(firstEl, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(lastEl);

    // 无子级可聚焦元素时（且关闭按钮被隐藏），Tab 不逃逸出抽屉
    rerender(
      <>
        <button type="button">背景按钮</button>
        <Drawer open onClose={() => {}} title="空抽屉">
          <p>纯文本内容</p>
        </Drawer>
      </>,
    );
    const emptyDialog = screen.getByRole("dialog");
    emptyDialog.querySelectorAll("button").forEach(btn => btn.setAttribute("disabled", "true"));
    emptyDialog.focus();
    fireEvent.keyDown(emptyDialog, { key: "Tab" });
    expect(emptyDialog.contains(document.activeElement) || document.activeElement === emptyDialog).toBe(true);
  });

  it.each([["Select", Select], ["SearchSelect", SearchSelect]] as const)("%s 在 Drawer 内按 Escape 只关闭选项层，不关闭 Drawer", async (_name, Control) => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <Drawer open={open} onClose={() => { onClose(); setOpen(false); }} title="筛选抽屉">
          <Control ariaLabel="存储目标" value="local" onChange={() => {}} options={[{ value: "local", label: "本地" }]} />
        </Drawer>
      );
    }
    render(<Harness />);
    const trigger = screen.getByRole("combobox", { name: "存储目标" });
    await user.click(trigger);
    expect(screen.getByRole("option", { name: "本地" })).not.toBeNull();

    fireEvent.keyDown(document.activeElement!, { key: "Escape", keyCode: 27 });
    await waitFor(() => expect(screen.queryByRole("option")).toBeNull());
    expect(onClose).not.toHaveBeenCalled();

    fireEvent.keyDown(trigger, { key: "Escape", keyCode: 27 });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("Popconfirm 在 Drawer 内按 Escape 只关闭气泡，不关闭外层 Drawer", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <Drawer open={open} onClose={() => { onClose(); setOpen(false); }} title="失败队列">
          <Popconfirm title="确认删除？" onConfirm={() => {}}>
            <Button>删除</Button>
          </Popconfirm>
        </Drawer>
      );
    }
    render(<Harness />);
    const trigger = screen.getByRole("button", { name: "删除" });
    await user.click(trigger);
    expect(screen.getByRole("alertdialog")).not.toBeNull();

    fireEvent.keyDown(screen.getByRole("button", { name: "取消" }), { key: "Escape", keyCode: 27 });
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(onClose).not.toHaveBeenCalled();

    fireEvent.keyDown(trigger, { key: "Escape", keyCode: 27 });
    expect(onClose).toHaveBeenCalledTimes(1);
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
    await waitFor(() => expect(screen.queryByText("确认删除？")).toBeNull());
  });

  it("暴露 alertdialog 语义并关联标题/说明", async () => {
    const user = userEvent.setup();
    render(
      <Popconfirm title="确认删除？" description="删除后不可恢复" onConfirm={() => {}}>
        <Button>删除</Button>
      </Popconfirm>,
    );

    await user.click(screen.getByRole("button", { name: "删除" }));

    // 不可逆操作：用 alertdialog 而不是普通 dialog，并显式关联标题与说明
    const dialog = screen.getByRole("alertdialog");
    const labelledBy = dialog.getAttribute("aria-labelledby");
    const describedBy = dialog.getAttribute("aria-describedby");
    expect(labelledBy).not.toBeNull();
    expect(describedBy).not.toBeNull();
    expect(document.getElementById(labelledBy as string)?.textContent).toBe("确认删除？");
    expect(document.getElementById(describedBy as string)?.textContent).toBe("删除后不可恢复");
  });

  it("disabled 时不挂弹出行为，且切换 disabled 不卸载子组件树", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onMount = vi.fn();
    const onUnmount = vi.fn();
    function TrackedButton(props: React.ComponentProps<typeof Button>) {
      useEffect(() => {
        onMount();
        return () => onUnmount();
      }, []);
      return <Button {...props}>删除</Button>;
    }
    function Harness() {
      const [disabled, setDisabled] = useState(true);
      return (
        <>
          <Popconfirm title="确认删除？" disabled={disabled} onConfirm={onConfirm}>
            <TrackedButton />
          </Popconfirm>
          <button type="button" onClick={() => setDisabled(v => !v)}>切换禁用</button>
        </>
      );
    }
    render(<Harness />);

    const triggerBefore = screen.getByRole("button", { name: "删除" });
    await user.click(triggerBefore);
    expect(screen.queryByText("确认删除？")).toBeNull();
    expect(onConfirm).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "切换禁用" }));
    await user.click(screen.getByRole("button", { name: "切换禁用" }));
    expect(screen.getByRole("button", { name: "删除" })).toBe(triggerBefore);
    expect(onMount).toHaveBeenCalledTimes(1);
    expect(onUnmount).not.toHaveBeenCalled();
  });

  it("Escape 关闭气泡并恢复触发按钮焦点，disabled 置 true 时重置可见状态", async () => {
    const user = userEvent.setup();
    function Harness() {
      const [disabled, setDisabled] = useState(false);
      return (
        <>
          <Popconfirm title="确认删除？" disabled={disabled} onConfirm={() => {}}>
            <Button>删除</Button>
          </Popconfirm>
          <button type="button" onClick={() => setDisabled(v => !v)}>切换禁用</button>
        </>
      );
    }
    render(<Harness />);
    const trigger = screen.getByRole("button", { name: "删除" });
    await user.click(trigger);
    expect(screen.getByRole("alertdialog")).not.toBeNull();
    fireEvent.keyDown(screen.getByRole("button", { name: "取消" }), { key: "Escape", keyCode: 27 });
    await waitFor(() => expect(screen.queryByText("确认删除？")).toBeNull());
    expect(document.activeElement).toBe(trigger);

    await user.click(trigger);
    expect(screen.getByText("确认删除？")).not.toBeNull();
    await user.click(screen.getByRole("button", { name: "切换禁用" }));
    await waitFor(() => expect(screen.queryByText("确认删除？")).toBeNull());
    await user.click(screen.getByRole("button", { name: "切换禁用" }));
    expect(screen.queryByText("确认删除？")).toBeNull();
  });
});

describe("SimplePopover", () => {
  it("supports side and align props without breaking click toggle", async () => {
    const user = userEvent.setup();
    render(
      <SimplePopover side="bottom" align="end" trigger={<Button>打开浮层</Button>}>
        <p>浮层内容</p>
      </SimplePopover>,
    );
    await user.click(screen.getByRole("button", { name: "打开浮层" }));
    expect(await screen.findByText("浮层内容")).not.toBeNull();
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
