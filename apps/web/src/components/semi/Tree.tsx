import "@douyinfe/semi-ui/react19-adapter";
import React, { useLayoutEffect, useRef, useState } from "react";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import STree from "@douyinfe/semi-ui/lib/es/tree";
import { cn } from "../../lib/cn";
import { t } from "../../lib/i18n";

export interface TreeNode {
  key: string;
  title: React.ReactNode;
  isLeaf?: boolean;
  children?: TreeNode[];
}

export function Tree({ nodes, expandedKeys, selectedKey, onExpandedChange, onSelect, loadChildren, onLoadError, className }: {
  nodes: TreeNode[];
  expandedKeys: string[];
  selectedKey?: string;
  onExpandedChange: (keys: string[]) => void;
  onSelect: (node: TreeNode) => void;
  loadChildren?: (node: TreeNode) => void | Promise<void>;
  onLoadError?: (node: TreeNode, e: unknown) => void;
  className?: string;
}) {
  const root = useRef<HTMLDivElement>(null);
  const [focused, setFocused] = useState<string>();
  const expanded = useRef(expandedKeys);
  expanded.current = expandedKeys;
  const [loading, setLoading] = useState<string[]>([]);
  const visible: TreeNode[] = [];
  const parents = new Map<string, string>();

  function visit(items: TreeNode[], parent?: string) {
    for (const item of items) {
      visible.push(item);
      if (parent) parents.set(item.key, parent);
      if (expandedKeys.includes(item.key)) visit(item.children ?? [], item.key);
    }
  }
  visit(nodes);

  useLayoutEffect(() => {
    root.current?.querySelector('[role="tree"]')?.setAttribute("aria-label", t("目录树"));
    for (const item of root.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? []) {
      const node = visible.find(node => node.key === item.dataset.key);
      item.tabIndex = node?.key === (visible.some(node => node.key === focused) ? focused : visible[0]?.key) ? 0 : -1;
      if (node?.isLeaf) item.removeAttribute("aria-expanded");
    }
  });

  async function expand(node: TreeNode, open: boolean) {
    if (open && loadChildren && !node.children && !node.isLeaf) {
      if (loading.includes(node.key)) return;
      setLoading(current => [...current, node.key]);
      try {
        await loadChildren(node);
      } catch (error) {
        onLoadError?.(node, error);
        return;
      } finally {
        setLoading(current => current.filter(key => key !== node.key));
      }
    }
    expanded.current = open ? [...new Set([...expanded.current, node.key])] : expanded.current.filter(key => key !== node.key);
    onExpandedChange(expanded.current);
  }

  function treeData(items: TreeNode[]): TreeNode[] {
    return items.map(node => ({
      ...node,
      isLeaf: node.isLeaf ?? (node.children ? node.children.length === 0 : !loadChildren),
      children: node.children ? treeData(node.children) : undefined,
    }));
  }

  return <div ref={root} onFocus={e => {
    const item = (e.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]');
    if (item) setFocused(item.dataset.key);
  }} onKeyDown={e => {
    const item = (e.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]');
    const index = visible.findIndex(node => node.key === item?.dataset.key);
    if (index < 0) return;
    const node = visible[index];
    let next: string | undefined;
    if (e.key === "ArrowDown") next = visible[Math.min(index + 1, visible.length - 1)]?.key;
    else if (e.key === "ArrowUp") next = visible[Math.max(index - 1, 0)]?.key;
    else if (e.key === "Home") next = visible[0]?.key;
    else if (e.key === "End") next = visible.at(-1)?.key;
    else if (e.key === "ArrowRight" && !node.isLeaf) {
      if (expandedKeys.includes(node.key)) next = node.children?.[0]?.key;
      else void expand(node, true);
    } else if (e.key === "ArrowLeft") {
      if (expandedKeys.includes(node.key)) void expand(node, false);
      else next = parents.get(node.key);
    } else if (e.key === "Enter" || e.key === " ") onSelect(node);
    else return;
    e.preventDefault();
    e.stopPropagation();
    if (next) {
      setFocused(next);
      Array.from(root.current?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? []).find(item => item.dataset.key === next)?.focus();
    }
  }}>
    <STree
      treeData={treeData(nodes)}
      keyMaps={{ label: "title", value: "key" }}
      loadData={loadChildren ? async () => {} : undefined}
      expandAction="doubleClick"
      expandedKeys={expandedKeys}
      value={selectedKey}
      onExpand={(_keys, data) => {
        const node = data.node as TreeNode;
        void expand(node, data.expanded === true);
      }}
      onSelect={(_keys, _selected, node) => onSelect(node as TreeNode)}
      renderLabel={(label, node) => <span>{label}{loading.includes(String(node?.key)) && <Spin size="small" />}</span>}
      className={cn(className)}
      motion={false}
    />
  </div>;
}
