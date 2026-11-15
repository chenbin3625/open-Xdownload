// @vitest-environment jsdom
import { act, render, screen } from "@testing-library/react";
import React from "react";
import { Notification } from "@douyinfe/semi-ui";
import { afterEach, describe, expect, it } from "vitest";
import { Toaster, toast } from "../semi";

afterEach(() => {
  act(() => { Notification.destroyAll(); });
});

describe("toast", () => {
  it("在 React 树之外命令式调用即可渲染", () => {
    render(<Toaster />);

    act(() => {
      toast.success({ message: "任务已取消" });
    });

    expect(screen.getByText("任务已取消")).not.toBeNull();
  });

  it("同时存在多条", () => {
    render(<Toaster />);

    act(() => {
      toast.info({ message: "第一条" });
      toast.error({ message: "第二条", description: "失败原因" });
    });

    expect(screen.getByText("第一条")).not.toBeNull();
    expect(screen.getByText("第二条")).not.toBeNull();
    expect(screen.getByText("失败原因")).not.toBeNull();
  });

  it("不同类型通知均可渲染", () => {
    render(<Toaster />);

    act(() => {
      toast.warning({ message: "警告信息" });
    });

    expect(screen.getByText("警告信息")).not.toBeNull();
  });
});
