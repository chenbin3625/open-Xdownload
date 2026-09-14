// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";
import { describe, expect, it } from "vitest";
import { Table, type TableColumn } from "./Table";

interface Row {
  id: number;
  name: string;
}

const rows: Row[] = [
  { id: 1, name: "第一行" },
  { id: 2, name: "第二行" },
];

const columns: TableColumn<Row>[] = [
  { key: "name", title: "名称", render: (record) => record.name },
  { key: "id", title: "ID", align: "right", render: (record) => `#${record.id}` },
];

describe("Table", () => {
  it("渲染表头与数据行", () => {
    render(<Table columns={columns} data={rows} rowKey={(row) => row.id} />);

    expect(screen.getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual([
      "名称",
      "ID",
    ]);
    expect(screen.getByText("第一行")).not.toBeNull();
    expect(screen.getByText("#2")).not.toBeNull();
  });

  it("空数据展示占位文案而非空白表格", () => {
    render(
      <Table columns={columns} data={[]} rowKey={(row) => row.id} emptyText="还没有任务" />,
    );
    expect(screen.getByText("还没有任务")).not.toBeNull();
  });

  it("展开行按下才渲染，并同步 aria-expanded", async () => {
    const user = userEvent.setup();
    render(
      <Table
        columns={columns}
        data={rows}
        rowKey={(row) => row.id}
        expandedRowRender={(record) => <p>详情 {record.name}</p>}
      />,
    );

    expect(screen.queryByText("详情 第一行")).toBeNull();

    const toggle = screen.getAllByRole("button", { name: "展开详情" })[0];
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    await user.click(toggle);
    expect(screen.getByText("详情 第一行")).not.toBeNull();
    expect(screen.getByRole("button", { name: "收起详情" }).getAttribute("aria-expanded")).toBe(
      "true",
    );

    // 展开态按行独立，展开第一行不影响第二行
    expect(screen.queryByText("详情 第二行")).toBeNull();

    await user.click(screen.getByRole("button", { name: "收起详情" }));
    expect(screen.queryByText("详情 第一行")).toBeNull();
  });

  it("展开按钮的 aria-controls 指向实际渲染的展开行", async () => {
    const user = userEvent.setup();
    render(
      <Table
        columns={columns}
        data={rows}
        rowKey={(row) => row.id}
        expandedRowRender={() => <p>详情</p>}
      />,
    );

    const toggle = screen.getAllByRole("button", { name: "展开详情" })[0];
    const panelId = toggle.getAttribute("aria-controls");
    await user.click(toggle);

    expect(panelId).not.toBeNull();
    expect(document.getElementById(panelId as string)).not.toBeNull();
  });

  it("不传 expandedRowRender 时不渲染展开列", () => {
    render(<Table columns={columns} data={rows} rowKey={(row) => row.id} />);
    expect(screen.queryByRole("button", { name: "展开详情" })).toBeNull();
    expect(screen.getAllByRole("columnheader")).toHaveLength(2);
  });
});
