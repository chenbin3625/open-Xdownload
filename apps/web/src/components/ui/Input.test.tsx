// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React, { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Field, Input, NumberInput, PasswordInput } from "./Input";

describe("Input", () => {
  it("onPressEnter 只在回车时触发", async () => {
    const user = userEvent.setup();
    const onPressEnter = vi.fn();
    render(<Input aria-label="路径" onPressEnter={onPressEnter} />);

    const input = screen.getByLabelText("路径");
    await user.type(input, "abc");
    expect(onPressEnter).not.toHaveBeenCalled();

    await user.type(input, "{Enter}");
    expect(onPressEnter).toHaveBeenCalledTimes(1);
  });

  it("allowClear 有值才显示清除按钮，点击后清空", async () => {
    const user = userEvent.setup();
    function Harness() {
      const [value, setValue] = useState("");
      return (
        <Input
          aria-label="关键词"
          allowClear
          value={value}
          onChange={(event) => setValue(event.target.value)}
        />
      );
    }
    render(<Harness />);

    expect(screen.queryByRole("button", { name: "清空" })).toBeNull();

    const input = screen.getByLabelText("关键词");
    await user.type(input, "hello");
    await user.click(screen.getByRole("button", { name: "清空" }));

    expect((input as HTMLInputElement).value).toBe("");
  });
});

describe("PasswordInput", () => {
  it("切换明文可见性并同步 aria-pressed", async () => {
    const user = userEvent.setup();
    render(<PasswordInput aria-label="Cookie" />);

    const input = screen.getByLabelText("Cookie");
    expect(input.getAttribute("type")).toBe("password");

    const toggle = screen.getByRole("button", { name: /显示|隐藏/ });
    await user.click(toggle);
    expect(input.getAttribute("type")).toBe("text");
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
  });
});

describe("NumberInput", () => {
  it("清空输入回传 null 而不是 NaN", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<NumberInput aria-label="并发数" value={5} onChange={onChange} />);

    await user.clear(screen.getByLabelText("并发数"));
    expect(onChange).toHaveBeenLastCalledWith(null);
  });

  it("失焦时把越界值夹回区间", async () => {
    const user = userEvent.setup();
    function Harness() {
      const [value, setValue] = useState<number | null>(5);
      return (
        <>
          <NumberInput aria-label="并发数" min={1} max={10} value={value} onChange={setValue} />
          <button type="button">别处</button>
        </>
      );
    }
    render(<Harness />);

    const input = screen.getByLabelText("并发数");
    await user.clear(input);
    await user.type(input, "99");
    await user.click(screen.getByRole("button", { name: "别处" }));

    expect((input as HTMLInputElement).value).toBe("10");
  });

  it("方向键按 step 增减", async () => {
    const user = userEvent.setup();
    function Harness() {
      const [value, setValue] = useState<number | null>(4);
      return <NumberInput aria-label="并发数" min={0} max={10} step={2} value={value} onChange={setValue} />;
    }
    render(<Harness />);

    const input = screen.getByLabelText("并发数");
    input.focus();
    await user.keyboard("{ArrowUp}");
    expect((input as HTMLInputElement).value).toBe("6");

    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect((input as HTMLInputElement).value).toBe("2");
  });
});

describe("Field", () => {
  it("传 htmlFor 时 label 与控件关联，点击标签聚焦输入框", async () => {
    const user = userEvent.setup();
    render(
      <Field label="保存目录" htmlFor="dir">
        <input id="dir" />
      </Field>,
    );

    await user.click(screen.getByText("保存目录"));
    expect(document.activeElement?.id).toBe("dir");
  });

  it("不传 htmlFor 时不产生悬空的 label 关联", () => {
    render(
      <Field label="仅说明">
        <input aria-label="内部输入" />
      </Field>,
    );
    // 悬空的 htmlFor 会让读屏朗读指向不存在元素的标签，比没有 label 更糟
    expect(document.querySelector("label")).toBeNull();
  });
});
