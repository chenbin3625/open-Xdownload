// @vitest-environment jsdom
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React, { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Checkbox, Select, Switch, Tabs } from "../semi";
import { Field } from "../semi";
import { SearchSelect } from "../semi";

const options = [
  { value: "all", label: "全部" },
  { value: "user", label: "用户归档" },
  { value: "list", label: "列表归档" },
];

describe("Switch", () => {
  it("点击回传布尔值", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Switch checked={false} onChange={onChange} ariaLabel="启用" />);

    await user.click(screen.getByRole("switch", { name: "启用" }));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("loading 期间禁用，避免重复提交", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Switch checked={false} onChange={onChange} loading ariaLabel="启用" />);

    await user.click(screen.getByRole("switch", { name: "启用" }));
    expect(onChange).not.toHaveBeenCalled();
  });
});

describe("Select", () => {
  it("uses the surrounding Field id so its label is associated", () => {
    render(
      <Field label="文件命名策略">
        <Select
          value="tweet_text"
          onChange={() => {}}
          options={[{ value: "tweet_text", label: "仅推文" }]}
        />
      </Field>,
    );

    const label = screen.getByText("文件命名策略");
    const trigger = screen.getByRole("combobox");
    expect(trigger.id).not.toBe("");
    expect(label.closest("label")?.htmlFor).toBe(trigger.id);
  });
});

describe("Checkbox", () => {
  it("点击标签也能切换（label 与控件已关联）", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Checkbox checked={false} onChange={onChange} label="同时删除本地文件" />);

    await user.click(screen.getByText("同时删除本地文件"));
    expect(onChange).toHaveBeenCalledWith(true);
  });
});

describe("Tabs", () => {
  it("只渲染选中面板的内容", async () => {
    const user = userEvent.setup();
    function Harness() {
      const [value, setValue] = useState("a");
      return (
        <Tabs
          value={value}
          onChange={setValue}
          items={[
            { key: "a", label: "标签 A", children: <p>内容 A</p> },
            { key: "b", label: "标签 B", children: <p>内容 B</p> },
          ]}
        />
      );
    }
    render(<Harness />);

    expect(screen.getByText("内容 A")).not.toBeNull();
    expect(screen.queryByText("内容 B")).toBeNull();

    await user.click(screen.getByRole("tab", { name: "标签 B" }));
    expect(screen.getByText("内容 B")).not.toBeNull();
    expect(screen.queryByText("内容 A")).toBeNull();
  });
});

describe("SearchSelect", () => {
  it("按关键词过滤选项", async () => {
    const user = userEvent.setup();
    render(<SearchSelect value="all" onChange={() => {}} options={options} ariaLabel="筛选" />);

    await user.click(screen.getByRole("combobox", { name: "筛选" }));
    const search = screen.getByRole("combobox", { name: "筛选 搜索..." });
    expect(document.activeElement).toBe(search);
    expect(search.getAttribute("aria-autocomplete")).toBe("list");
    const list = document.getElementById(search.getAttribute("aria-controls")!);
    expect(list?.getAttribute("role")).toBe("listbox");
    await user.type(search, "列表");

    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option", { name: "列表归档" })).not.toBeNull();
  });

  it("无匹配时给出提示而不是空列表", async () => {
    const user = userEvent.setup();
    render(<SearchSelect value="all" onChange={() => {}} options={options} ariaLabel="筛选" />);

    await user.click(screen.getByRole("combobox", { name: "筛选" }));
    await user.type(screen.getByPlaceholderText("搜索..."), "zzz");

    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText("无匹配项")).not.toBeNull();
  });

  it("方向键移动高亮，Enter 提交并关闭", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<SearchSelect value="all" onChange={onChange} options={options} ariaLabel="筛选" />);

    await user.click(screen.getByRole("combobox", { name: "筛选" }));
    const search = screen.getByPlaceholderText("搜索...");
    expect(screen.getByRole("combobox", { name: "筛选" }).getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement).toBe(search);

    fireEvent.keyDown(search, { key: "ArrowDown", keyCode: 40 });
    // aria-activedescendant 必须指向真实存在的 option，否则读屏读不出高亮
    const activeId = search.getAttribute("aria-activedescendant");
    expect(activeId).not.toBeNull();
    expect(document.getElementById(activeId as string)).not.toBeNull();

    fireEvent.keyDown(search, { key: "Enter", keyCode: 13 });
    expect(onChange).toHaveBeenCalledWith("user");
    expect(screen.queryByPlaceholderText("搜索...")).toBeNull();
  });

  it("展开时高亮定位到当前选中项", async () => {
    const user = userEvent.setup();
    render(<SearchSelect value="list" onChange={() => {}} options={options} ariaLabel="筛选" />);

    await user.click(screen.getByRole("combobox", { name: "筛选" }));
    expect(screen.getByRole("option", { name: "列表归档" }).getAttribute("aria-selected")).toBe(
      "true",
    );
  });

  it("使用 Semi 的 small 尺寸", () => {
    render(<SearchSelect value="all" size="sm" onChange={() => {}} options={options} ariaLabel="小号选择器" />);
    const trigger = screen.getByRole("combobox", { name: "小号选择器" });
    expect(trigger.className).toContain("semi-select-small");
  });
});

