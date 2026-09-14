// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { describe, expect, it, vi } from "vitest";
import { Pagination, buildPageItems } from "./Pagination";

describe("buildPageItems", () => {
  it("总页数不超过 7 时全部列出，不折叠", () => {
    expect(buildPageItems(1, 7)).toEqual([1, 2, 3, 4, 5, 6, 7]);
  });

  it("当前页在中间时两侧各折叠一次", () => {
    expect(buildPageItems(10, 20)).toEqual([1, "gap", 9, 10, 11, "gap", 20]);
  });

  it("当前页贴近首页时只在右侧折叠", () => {
    expect(buildPageItems(2, 20)).toEqual([1, 2, 3, "gap", 20]);
  });

  it("当前页贴近末页时只在左侧折叠", () => {
    expect(buildPageItems(19, 20)).toEqual([1, "gap", 18, 19, 20]);
  });

  it("相邻页码之间不插入无意义的省略号", () => {
    // 4 与 1 之间只差 2，展开成 2/3 更自然，但至少不能出现代表单页的 gap
    expect(buildPageItems(3, 20)).toEqual([1, 2, 3, 4, "gap", 20]);
  });
});

describe("Pagination", () => {
  it("首页禁用上一页，末页禁用下一页", () => {
    const { unmount } = render(
      <Pagination page={1} pageSize={10} total={100} onPageChange={() => {}} />,
    );
    expect(screen.getByRole("button", { name: "上一页" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "下一页" }).hasAttribute("disabled")).toBe(false);
    unmount();

    render(<Pagination page={10} pageSize={10} total={100} onPageChange={() => {}} />);
    expect(screen.getByRole("button", { name: "下一页" }).hasAttribute("disabled")).toBe(true);
  });

  it("当前页用 aria-current 标记", () => {
    render(<Pagination page={3} pageSize={10} total={100} onPageChange={() => {}} />);
    expect(screen.getByRole("button", { name: "第 3 页" }).getAttribute("aria-current")).toBe("page");
  });

  it("点击页码回传目标页", async () => {
    const user = userEvent.setup();
    const onPageChange = vi.fn();
    render(<Pagination page={1} pageSize={10} total={100} onPageChange={onPageChange} />);

    await user.click(screen.getByRole("button", { name: "第 2 页" }));
    expect(onPageChange).toHaveBeenCalledWith(2);

    await user.click(screen.getByRole("button", { name: "下一页" }));
    expect(onPageChange).toHaveBeenLastCalledWith(2);
  });

  it("page 超出范围时收敛到最后一页，不会渲染越界页码", () => {
    // 后端 total 变小而前端 page 还停在旧值时不能崩
    render(<Pagination page={99} pageSize={10} total={30} onPageChange={() => {}} />);
    expect(screen.getByRole("button", { name: "第 3 页" }).getAttribute("aria-current")).toBe("page");
    expect(screen.queryByRole("button", { name: "第 4 页" })).toBeNull();
  });

  it("total 为 0 时仍渲染第 1 页且两侧翻页均禁用", () => {
    render(<Pagination page={1} pageSize={10} total={0} onPageChange={() => {}} />);
    expect(screen.getByRole("button", { name: "第 1 页" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "上一页" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "下一页" }).hasAttribute("disabled")).toBe(true);
  });

  it("未传 onPageSizeChange 时不渲染每页条数选择器", () => {
    render(<Pagination page={1} pageSize={10} total={100} onPageChange={() => {}} />);
    expect(screen.queryByRole("combobox", { name: "每页条数" })).toBeNull();
  });

  it("totalLabel 自定义总数文案", () => {
    render(
      <Pagination
        page={1}
        pageSize={10}
        total={42}
        onPageChange={() => {}}
        totalLabel={(total) => `共 ${total} 个任务`}
      />,
    );
    expect(screen.getByText("共 42 个任务")).not.toBeNull();
  });
});
