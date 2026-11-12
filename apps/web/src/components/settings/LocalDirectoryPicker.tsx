import { t, useTranslation } from "../../lib/i18n";
import { ArrowUp, Folder, FolderPlus, RefreshCw } from "lucide-react";
import { useMutation, useQuery } from "@tanstack/react-query";
import React, { useEffect, useRef, useState } from "react";
import {
  createLocalDirectory,
  listLocalDirectories,
  type LocalDirectoryListing,
} from "../../lib/api";
import { getErrorMessage, notifyError } from "../common/CommonUI";
import { Button, Card, Empty, Typography } from "@douyinfe/semi-ui";
import { Alert } from "../semi";
import { Input } from "../semi";
import { Tooltip } from "../semi";
import { Spinner } from "../semi";
import { toast } from "../semi";
import { Tree, type TreeNode } from "../semi";

export type DirectoryTreeNode = TreeNode & {
  path: string;
  children?: DirectoryTreeNode[];
};

export function LocalDirectoryPicker({
  path,
  onSelect,
}: {
  path: string;
  onSelect: (path: string) => void;
}) {
  useTranslation();
  const [rootPath, setRootPath] = useState(path);
  const [selectedPath, setSelectedPath] = useState(path);
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const [treeData, setTreeData] = useState<DirectoryTreeNode[]>([]);
  const [inlineError, setInlineError] = useState("");
  const nodeLoadControllerRef = useRef<AbortController | null>(null);

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
      toast.success({ message: t("目录已创建并选择") });
    },
    onError: notifyError(t("创建目录失败")),
  });

  const resolvedPath = listing.isError ? rootPath : listing.data?.path ?? rootPath;

  useEffect(() => {
    setRootPath(path);
    setSelectedPath(path);
  }, [path]);

  useEffect(() => {
    const current = listing.data;
    if (listing.isError || !current) {
      setTreeData([]);
      setExpandedKeys([]);
      setInlineError("");
      return;
    }
    const rootNode = listingToDirectoryTreeRoot(current);
    setTreeData([rootNode]);
    setExpandedKeys([rootNode.key]);
    setSelectedPath(rootNode.path);
    setInlineError("");
  }, [listing.data, listing.isError]);

  useEffect(() => {
    const controller = new AbortController();
    nodeLoadControllerRef.current = controller;
    return () => controller.abort();
  }, []);

  async function loadDirectoryNode(node: TreeNode) {
    const dirNode = node as DirectoryTreeNode;
    if (dirNode.children || dirNode.isLeaf) {
      return;
    }
    const controller = nodeLoadControllerRef.current;
    try {
      const childListing = await listLocalDirectories(dirNode.path, controller?.signal);
      if (controller?.signal.aborted) return;
      const children = childListing.entries.map(directoryEntryToTreeNode);
      setTreeData((current) => updateDirectoryTreeChildren(current, dirNode.key, children));
      setInlineError("");
    } catch (error) {
      if (controller?.signal.aborted) return;
      const message = getErrorMessage(error);
      setInlineError(message);
    }
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
    <div className="flex w-full min-w-0 flex-col gap-2.5">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <div className="min-w-0 flex-1 basis-60">
          <Input
            aria-label={t("下载目录")}
            prefix={<Folder className="size-4 text-fg-subtle" />}
            value={selectedPath}
            onChange={(event) => setSelectedPath(event.target.value)}
            onPressEnter={openSelectedPath}
            placeholder="/path/to/downloads"
          />
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button
            type="tertiary"
            onClick={openSelectedPath}
            disabled={!selectedPath.trim() || listing.isLoading}
          >
            {t("打开")}</Button>
          <Button
            type="tertiary"
            icon={<FolderPlus className="size-3.5" />}
            onClick={createSelectedPath}
            loading={createDirectory.isPending}
            disabled={!selectedPath.trim() || createDirectory.isPending}
            aria-busy={createDirectory.isPending}
          >
            {t("创建")}</Button>
          <Button
            theme="solid"
            onClick={() => onSelect(selectedPath.trim())}
            disabled={!selectedPath.trim()}
          >
            {t("选择此目录")}</Button>
        </div>
      </div>

      <Card bodyStyle={{ padding: 16 }}>
        <div className="flex w-full min-w-0 flex-col gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <Button
              type="tertiary"
              icon={<ArrowUp className="size-3.5" />}
              disabled={!listing.data?.parent || listing.isLoading || listing.isError}
              onClick={() => listing.data?.parent && setRootPath(listing.data.parent)}
            >
              {t("上级目录")}</Button>
            <Typography.Text
              type="tertiary"
              title={resolvedPath}
              className="min-w-0 flex-1 truncate px-2 font-mono text-xs"
            >
              {resolvedPath}
            </Typography.Text>
            <Tooltip content={t("刷新目录")}>
              <Button
                theme="borderless"
                type="tertiary"
                icon={<RefreshCw className={`size-3.5 ${listing.isFetching ? "animate-spin" : ""}`} />}
                onClick={() => listing.refetch()}
                aria-label={t("刷新目录")}
              />
            </Tooltip>
          </div>

          {listing.isError && (
            <Alert
              type="error"
              message={t("读取目录失败")}
              description={listing.error.message}
            />
          )}

          {inlineError && !listing.isError && (
            <Alert
              type="warning"
              message={t("读取子目录失败")}
              description={inlineError}
            />
          )}

          <div className="directory-tree-panel">
            {listing.isLoading ? (
              <div className="flex h-24 items-center justify-center">
                <Spinner className="size-5" />
              </div>
            ) : !listing.isError && treeData.length > 0 ? (
              <Tree
                nodes={treeData}
                expandedKeys={expandedKeys}
                selectedKey={selectedPath}
                onExpandedChange={setExpandedKeys}
                onSelect={(node) => setSelectedPath((node as DirectoryTreeNode).path)}
                loadChildren={loadDirectoryNode}
              />
            ) : (
              <Empty description={t("没有子目录")} className="py-6" />
            )}
          </div>
        </div>
      </Card>
    </div>
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

export function directoryEntryToTreeNode(
  entry: LocalDirectoryListing["entries"][number],
): DirectoryTreeNode {
  return {
    key: entry.path,
    path: entry.path,
    title: entry.name,
    isLeaf: !entry.hasChildren,
  };
}

export function updateDirectoryTreeChildren(
  nodes: DirectoryTreeNode[],
  targetKey: string,
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
