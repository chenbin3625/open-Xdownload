import { useMutation, useQuery } from "@tanstack/react-query";
import React, { useEffect, useRef, useState } from "react";
import {
  createLocalDirectory,
  listLocalDirectories,
  type LocalDirectoryListing,
} from "../../lib/api";
import { getErrorMessage } from "../../lib/format";
import { toast } from "../../lib/toast";

export type DirectoryTreeNode = {
  key: string;
  path: string;
  title: string;
  children?: DirectoryTreeNode[];
  isLeaf?: boolean;
};

export function LocalDirectoryPicker({ path, onSelect }: { path: string; onSelect: (path: string) => void }) {
  const [rootPath, setRootPath] = useState(path);
  const [selectedPath, setSelectedPath] = useState(path);
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const [treeData, setTreeData] = useState<DirectoryTreeNode[]>([]);
  const directoryControllers = useRef(new Map<string, AbortController>());
  const listing = useQuery<LocalDirectoryListing>({
    queryKey: ["local-directories", rootPath],
    queryFn: ({ signal }) => listLocalDirectories(rootPath, signal),
  });
  const createDirectory = useMutation({
    mutationFn: createLocalDirectory,
    onSuccess: (data) => {
      setRootPath(data.path);
      setSelectedPath(data.path);
      onSelect(data.path);
      const rootNode = listingToDirectoryTreeRoot(data);
      setTreeData([rootNode]);
      setExpandedKeys([rootNode.key]);
      toast("目录已创建并选择");
    },
    onError: (error) => {
      toast("创建目录失败", { description: getErrorMessage(error), tone: "err" });
    },
  });
  const resolvedPath = listing.data?.path ?? rootPath;

  useEffect(() => {
    setRootPath(path);
    setSelectedPath(path);
  }, [path]);

  useEffect(() => {
    if (!listing.data) {
      return;
    }
    const rootNode = listingToDirectoryTreeRoot(listing.data);
    setTreeData([rootNode]);
    setExpandedKeys([rootNode.key]);
    setSelectedPath(rootNode.path);
  }, [listing.data]);

  async function loadDirectoryNode(node: DirectoryTreeNode) {
    if (node.children || node.isLeaf) {
      return;
    }
    directoryControllers.current.get(node.key)?.abort();
    const controller = new AbortController();
    directoryControllers.current.set(node.key, controller);
    try {
      const childListing = await listLocalDirectories(node.path, controller.signal);
      const children = childListing.entries.map(directoryEntryToTreeNode);
      setTreeData((current) => updateDirectoryTreeChildren(current, node.key, children));
    } catch (error) {
      if (!(error instanceof Error && error.name === "AbortError")) {
        toast("读取目录失败", { description: getErrorMessage(error), tone: "err" });
      }
    } finally {
      if (directoryControllers.current.get(node.key) === controller) {
        directoryControllers.current.delete(node.key);
      }
    }
  }

  useEffect(() => () => {
    for (const controller of directoryControllers.current.values()) controller.abort();
    directoryControllers.current.clear();
  }, []);

  function toggleNode(node: DirectoryTreeNode) {
    const expanded = expandedKeys.includes(node.key);
    if (expanded) {
      setExpandedKeys((current) => current.filter((key) => key !== node.key));
      return;
    }
    setExpandedKeys((current) => [...current, node.key]);
    void loadDirectoryNode(node);
  }

  function openSelectedPath() {
    const trimmed = selectedPath.trim();
    if (!trimmed) return;
    setRootPath(trimmed);
  }

  function createSelectedPath() {
    const trimmed = selectedPath.trim();
    if (!trimmed) return;
    createDirectory.mutate(trimmed);
  }

  return (
    <div className="settings-stack">
      <div className="dir-path-row">
        <input
          className="parser-input"
          value={selectedPath}
          onChange={(event) => setSelectedPath(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              openSelectedPath();
            }
          }}
        />
        <button type="button" className="job-text-btn" disabled={!selectedPath.trim() || listing.isLoading} onClick={openSelectedPath}>
          打开
        </button>
        <button type="button" className="job-text-btn" disabled={!selectedPath.trim() || createDirectory.isPending} onClick={createSelectedPath}>
          {createDirectory.isPending ? "创建中…" : "创建"}
        </button>
        <button type="button" className="shell-primary-btn" disabled={!selectedPath.trim()} onClick={() => onSelect(selectedPath.trim())}>
          选择此目录
        </button>
      </div>

      <div className="dir-browser">
        <div className="failed-toolbar">
          <button
            type="button"
            className="job-text-btn"
            disabled={!listing.data?.parent || listing.isLoading}
            onClick={() => listing.data?.parent && setRootPath(listing.data.parent)}
          >
            上级
          </button>
          <span className="job-ellipsis" title={resolvedPath}>{resolvedPath}</span>
          <button type="button" className="job-text-btn" disabled={listing.isFetching} onClick={() => listing.refetch()}>
            {listing.isFetching ? "刷新中…" : "刷新"}
          </button>
        </div>
        {listing.isError ? <p className="shell-error">读取目录失败：{listing.error.message}</p> : null}
        {listing.isLoading && treeData.length === 0 ? (
          <div className="shell-skeleton-block" />
        ) : treeData.length > 0 ? (
          <DirectoryTreeView
            nodes={treeData}
            expandedKeys={expandedKeys}
            selectedPath={selectedPath}
            onSelect={setSelectedPath}
            onToggle={toggleNode}
          />
        ) : (
          <p className="job-empty">没有子目录</p>
        )}
      </div>
    </div>
  );
}

function DirectoryTreeView({
  expandedKeys,
  nodes,
  onSelect,
  onToggle,
  selectedPath,
}: {
  expandedKeys: string[];
  nodes: DirectoryTreeNode[];
  onSelect: (path: string) => void;
  onToggle: (node: DirectoryTreeNode) => void;
  selectedPath: string;
}) {
  return (
    <ul className="dir-tree">
      {nodes.map((node) => {
        const expanded = expandedKeys.includes(node.key);
        return (
          <li key={node.key}>
            <div className={selectedPath === node.path ? "dir-tree-row is-selected" : "dir-tree-row"}>
              {node.isLeaf ? (
                <span className="dir-tree-toggle" aria-hidden="true" />
              ) : (
                <button
                  type="button"
                  className="dir-tree-toggle"
                  aria-label={expanded ? "折叠" : "展开"}
                  onClick={() => onToggle(node)}
                >
                  {expanded ? "▾" : "▸"}
                </button>
              )}
              <button type="button" className="dir-tree-name" onClick={() => onSelect(node.path)}>
                {node.title}
              </button>
            </div>
            {expanded && node.children?.length ? (
              <DirectoryTreeView
                nodes={node.children}
                expandedKeys={expandedKeys}
                selectedPath={selectedPath}
                onSelect={onSelect}
                onToggle={onToggle}
              />
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}

export function listingToDirectoryTreeRoot(listing: LocalDirectoryListing): DirectoryTreeNode {
  return {
    key: listing.path,
    path: listing.path,
    title: listing.path,
    children: listing.entries.map(directoryEntryToTreeNode),
    isLeaf: listing.entries.length === 0,
  };
}

export function directoryEntryToTreeNode(entry: LocalDirectoryListing["entries"][number]): DirectoryTreeNode {
  return {
    key: entry.path,
    path: entry.path,
    title: entry.name,
    isLeaf: !entry.hasChildren,
  };
}

export function updateDirectoryTreeChildren(
  nodes: DirectoryTreeNode[],
  targetKey: React.Key,
  children: DirectoryTreeNode[],
): DirectoryTreeNode[] {
  return nodes.map((node) => {
    if (node.key === targetKey) {
      return { ...node, children, isLeaf: children.length === 0 };
    }
    if (node.children) {
      return { ...node, children: updateDirectoryTreeChildren(node.children, targetKey, children) };
    }
    return node;
  });
}
