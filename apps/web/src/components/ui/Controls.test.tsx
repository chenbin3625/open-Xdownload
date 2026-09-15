// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React, { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Button } from "./Button";
import { Checkbox, Segmented, Switch, Tabs } from "./Controls";
import { SearchSelect } from "./SearchSelect";

const options = [
  { value: "all", label: "全部" },
  { value: "user", label: "用户归档" },
  { value: "list", label: "列表归档" },
];

describe("Segmented", () => {
  it("暴露 radiogroup 语义并标记当前项", () => {
    render(<Segmented value="user" onChange={() => {}} options={options} ariaLabel="视图" />);
    expect(screen.getByRole("radiogroup", { name: "视图" })).not.toBeNull();
    expect(screen.getByRole("radio", { name: "用户归档" }).getAttribute("aria-checked")).toBe("true");
  });

  it("点击其他项切换，点当前项不回传空值", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Segmented value="user" onChange={onChange} options={options} />);

    await user.click(screen.getByRole("radio", { name: "列表归档" }));
    expect(onChange).toHaveBeenCalledWith("list");

    // ToggleGroup 允许取消选中，这里必须挡住空值，否则筛选器会被清空
    onChange.mockClear();
    await user.click(screen.getByRole("radio", { name: "用户归档" }));
    expect(onChange).not.toHaveBeenCalledWith("");
  });
});

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
    await user.type(screen.getByPlaceholderText("搜索..."), "列表");

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

    await user.keyboard("{ArrowDown}");
    // aria-activedescendant 必须指向真实存在的 option，否则读屏读不出高亮
    const activeId = search.getAttribute("aria-activedescendant");
    expect(activeId).not.toBeNull();
    expect(document.getElementById(activeId as string)).not.toBeNull();

    await user.keyboard("{Enter}");
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
});

describe("Button", () => {
  it("loading 时禁用并标记 aria-busy", () => {
    render(<Button loading>提交</Button>);
    const button = screen.getByRole("button", { name: "提交" });
    expect(button.hasAttribute("disabled")).toBe(true);
    expect(button.getAttribute("aria-busy")).toBe("true");
  });

  it("loading 时不触发 onClick", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <Button loading onClick={onClick}>
        提交
      </Button>,
    );
    await user.click(screen.getByRole("button", { name: "提交" }));
    expect(onClick).not.toHaveBeenCalled();
  });

  it("asChild 把样式套到子元素上，不额外包一层按钮", () => {
    render(
      <Button asChild>
        <a href="/docs">文档</a>
      </Button>,
    );
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByRole("link", { name: "文档" })).not.toBeNull();
  });

  it("asChild 支持 loading 状态", () => {
    render(
      <Button asChild loading>
        <a href="/docs">文档</a>
      </Button>,
    );
    const link = screen.getByRole("link", { name: "文档" });
    expect(link.getAttribute("aria-busy")).toBe("true");
    expect(link.getAttribute("aria-disabled")).toBe("true");
  });
});
