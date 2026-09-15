// @vitest-environment jsdom
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React, { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Tree, type TreeNode } from "./Tree";

// 受控外壳：把 expandedKeys / selectedKey 的状态放在测试侧，
// 与真实调用点（LocalDirectoryPicker）的用法一致。
function Harness({
  initial,
  loadChildren,
}: {
  initial: TreeNode[];
  loadChildren?: (node: TreeNode) => Promise<void> | void;
}) {
  const [nodes, setNodes] = useState(initial);
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const [selectedKey, setSelectedKey] = useState<string>();

  return (
    <Tree
      nodes={nodes}
      expandedKeys={expandedKeys}
      selectedKey={selectedKey}
      onExpandedChange={setExpandedKeys}
      onSelect={(node) => setSelectedKey(node.key)}
      loadChildren={
        loadChildren &&
        (async (node) => {
          await loadChildren(node);
          setNodes((current) =>
            current.map((item) =>
              item.key === node.key
                ? { ...item, children: [{ key: `${node.key}/child`, title: "child", isLeaf: true }] }
                : item,
            ),
          );
        })
      }
    />
  );
}

const twoRoots: TreeNode[] = [
  { key: "a", title: "a" },
  { key: "b", title: "b", isLeaf: true },
];

describe("Tree", () => {
  it("只渲染展开路径上的节点", async () => {
    const user = userEvent.setup();
    render(
      <Harness
        initial={[
          { key: "a", title: "a", children: [{ key: "a/1", title: "a1", isLeaf: true }] },
        ]}
      />,
    );

    expect(screen.queryByText("a1")).toBeNull();
    await user.dblClick(screen.getByText("a"));
    expect(screen.getByText("a1")).not.toBeNull();
  });

  it("展开未加载的节点时触发懒加载", async () => {
    const user = userEvent.setup();
    const loadChildren = vi.fn(() => Promise.resolve());
    render(<Harness initial={twoRoots} loadChildren={loadChildren} />);

    await user.dblClick(screen.getByText("a"));
    expect(loadChildren).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.getByText("child")).not.toBeNull());

    // 已加载过的节点再次展开不应重复请求
    await user.dblClick(screen.getByText("a"));
    await user.dblClick(screen.getByText("a"));
    expect(loadChildren).toHaveBeenCalledTimes(1);
  });

  it("叶子节点不暴露 aria-expanded", () => {
    render(<Harness initial={twoRoots} />);
    const items = screen.getAllByRole("treeitem");
    expect(items[0].getAttribute("aria-expanded")).toBe("false");
    expect(items[1].hasAttribute("aria-expanded")).toBe(false);
  });

  it("方向键移动焦点，右键展开、左键收起", async () => {
    const user = userEvent.setup();
    render(
      <Harness
        initial={[
          { key: "a", title: "a", children: [{ key: "a/1", title: "a1", isLeaf: true }] },
          { key: "b", title: "b", isLeaf: true },
        ]}
      />,
    );

    const items = screen.getAllByRole("treeitem");
    // roving tabindex：只有激活项可 Tab 到达
    expect(items[0].getAttribute("tabindex")).toBe("0");
    expect(items[1].getAttribute("tabindex")).toBe("-1");

    items[0].focus();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByText("a1")).not.toBeNull();

    await user.keyboard("{ArrowLeft}");
    expect(screen.queryByText("a1")).toBeNull();

    await user.keyboard("{ArrowDown}");
    expect(document.activeElement?.getAttribute("data-key")).toBe("b");
  });

  it("Enter 选中并同步 aria-selected", async () => {
    const user = userEvent.setup();
    render(<Harness initial={twoRoots} />);

    screen.getAllByRole("treeitem")[0].focus();
    await user.keyboard("{Enter}");
    expect(screen.getAllByRole("treeitem")[0].getAttribute("aria-selected")).toBe("true");
  });
});
