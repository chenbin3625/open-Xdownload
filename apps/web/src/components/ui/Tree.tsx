import { ChevronRight, Folder, FolderOpen } from "lucide-react";
import React, { useMemo, useRef, useState } from "react";
import { cn } from "../../lib/cn";
import { Spinner } from "./Spinner";

export interface TreeNode {
  key: string;
  title: React.ReactNode;
  isLeaf?: boolean;
  children?: TreeNode[];
}

interface FlatNode {
  node: TreeNode;
  level: number;
}

// 只把展开路径上的节点摊平：ARIA 允许 role=tree 下用扁平 treeitem +
// aria-level 表达层级，比嵌套 group 更好做方向键导航。
function flatten(nodes: TreeNode[], expanded: Set<string>, level = 1, out: FlatNode[] = []) {
  for (const node of nodes) {
    out.push({ node, level });
    if (node.children && expanded.has(node.key)) {
      flatten(node.children, expanded, level + 1, out);
    }
  }
  return out;
}

// 手写懒加载目录树，替代 antd Tree/DirectoryTree。
// 键盘：上下移动焦点、右展开、左收起、Enter/Space 选中。
export function Tree({
  nodes,
  expandedKeys,
  selectedKey,
  onExpandedChange,
  onSelect,
  loadChildren,
  className,
}: {
  nodes: TreeNode[];
  expandedKeys: string[];
  selectedKey?: string;
  onExpandedChange: (keys: string[]) => void;
  onSelect: (node: TreeNode) => void;
  loadChildren?: (node: TreeNode) => void | Promise<void>;
  className?: string;
}) {
  const [loadingKeys, setLoadingKeys] = useState<Set<string>>(new Set());
  const [activeKey, setActiveKey] = useState<string | undefined>(selectedKey);
  const containerRef = useRef<HTMLDivElement>(null);

  const expanded = useMemo(() => new Set(expandedKeys), [expandedKeys]);
  const visible = useMemo(() => flatten(nodes, expanded), [nodes, expanded]);

  // 焦点落点：优先当前激活项，其次选中项，最后第一项
  const activeIndex = Math.max(
    0,
    visible.findIndex((item) => item.node.key === (activeKey ?? selectedKey)),
  );

  // 按下标而非 key 定位：目录路径里可能有引号/空格等字符，
  // 拼进选择器需要 CSS.escape，而下标天然安全。
  function focusIndex(index: number) {
    const target = visible[index];
    if (!target) return;
    setActiveKey(target.node.key);
    containerRef.current?.querySelector<HTMLElement>(`[data-index="${index}"]`)?.focus();
  }

  async function expand(node: TreeNode) {
    onExpandedChange([...expandedKeys, node.key]);
    // 子节点尚未加载过才触发懒加载，避免每次展开都打一次请求
    if (node.children || node.isLeaf || !loadChildren) return;
    setLoadingKeys((current) => new Set(current).add(node.key));
    try {
      await loadChildren(node);
    } finally {
      setLoadingKeys((current) => {
        const next = new Set(current);
        next.delete(node.key);
        return next;
      });
    }
  }

  function collapse(node: TreeNode) {
    onExpandedChange(expandedKeys.filter((key) => key !== node.key));
  }

  function toggle(node: TreeNode) {
    if (node.isLeaf) return;
    if (expanded.has(node.key)) collapse(node);
    else void expand(node);
  }

  function handleKeyDown(event: React.KeyboardEvent) {
    const item = visible[activeIndex];
    if (!item) return;
    const { node } = item;

    if (event.key === "ArrowDown") {
      event.preventDefault();
      focusIndex(Math.min(activeIndex + 1, visible.length - 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      focusIndex(Math.max(activeIndex - 1, 0));
    } else if (event.key === "ArrowRight") {
      event.preventDefault();
      if (node.isLeaf) return;
      if (expanded.has(node.key)) focusIndex(activeIndex + 1);
      else void expand(node);
    } else if (event.key === "ArrowLeft") {
      event.preventDefault();
      if (!node.isLeaf && expanded.has(node.key)) {
        collapse(node);
        return;
      }
      // 已收起时左键回到父节点：向上找第一个层级更浅的节点
      for (let index = activeIndex - 1; index >= 0; index -= 1) {
        if (visible[index].level < item.level) {
          focusIndex(index);
          return;
        }
      }
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onSelect(node);
      toggle(node);
    } else if (event.key === "Home") {
      event.preventDefault();
      focusIndex(0);
    } else if (event.key === "End") {
      event.preventDefault();
      focusIndex(visible.length - 1);
    }
  }

  return (
    <div
      ref={containerRef}
      role="tree"
      aria-label="目录树"
      onKeyDown={handleKeyDown}
      className={cn("max-h-72 overflow-auto p-1", className)}
    >
      {visible.map((item, index) => {
        const { node, level } = item;
        const isExpanded = expanded.has(node.key);
        const isSelected = node.key === selectedKey;
        const isLoading = loadingKeys.has(node.key);
        return (
          <div
            key={node.key}
            data-index={index}
            data-key={node.key}
            role="treeitem"
            aria-level={level}
            aria-selected={isSelected}
            aria-expanded={node.isLeaf ? undefined : isExpanded}
            tabIndex={index === activeIndex ? 0 : -1}
            onFocus={() => setActiveKey(node.key)}
            onClick={() => {
              onSelect(node);
              setActiveKey(node.key);
            }}
            onDoubleClick={() => toggle(node)}
            style={{ paddingLeft: `${(level - 1) * 1.125 + 0.25}rem` }}
            className={cn(
              "flex cursor-pointer items-center gap-1 rounded-control py-1 pr-2",
              "text-sm whitespace-nowrap transition-colors select-none",
              isSelected ? "bg-brand-50 font-medium text-brand-700" : "text-fg-body hover:bg-surface-hover",
            )}
          >
            <span
              // 折叠箭头单独可点，避免点击文字就展开
              onClick={(event) => {
                event.stopPropagation();
                toggle(node);
              }}
              className={cn(
                "flex size-4 shrink-0 items-center justify-center text-fg-subtle",
                node.isLeaf && "invisible",
              )}
            >
              {isLoading ? (
                <Spinner className="size-3" />
              ) : (
                <ChevronRight
                  className={cn("size-3.5 transition-transform duration-150", isExpanded && "rotate-90")}
                />
              )}
            </span>
            {isExpanded && !node.isLeaf ? (
              <FolderOpen className="size-3.5 shrink-0 text-brand-500" />
            ) : (
              <Folder className="size-3.5 shrink-0 text-fg-subtle" />
            )}
            <span className="min-w-0 truncate">{node.title}</span>
          </div>
        );
      })}
    </div>
  );
}
