import { ArrowUp, Folder, FolderPlus, RefreshCw } from "lucide-react";
import { useMutation, useQuery } from "@tanstack/react-query";
import React, { useEffect, useRef, useState } from "react";
import {
  createLocalDirectory,
  listLocalDirectories,
  type LocalDirectoryListing,
} from "../../lib/api";
import {
  AppEmpty,
  EllipsisText,
  Stack,
  Toolbar,
  getErrorMessage,
  notifyError,
} from "../common/CommonUI";
import { Button } from "../ui/Button";
import { Card } from "../ui/Card";
import { Alert } from "../ui/Feedback";
import { Input } from "../ui/Input";
import { Tooltip } from "../ui/Overlay";
import { Spinner } from "../ui/Spinner";
import { toast } from "../ui/Toast";
import { Tree, type TreeNode } from "../ui/Tree";

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
  const [rootPath, setRootPath] = useState(path);
  const [selectedPath, setSelectedPath] = useState(path);
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const [treeData, setTreeData] = useState<DirectoryTreeNode[]>([]);
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
      toast.success({ message: "目录已创建并选择" });
    },
    onError: notifyError("创建目录失败"),
  });

  const resolvedPath = listing.data?.path ?? rootPath;

  useEffect(() => {
    setRootPath(path);
    setSelectedPath(path);
  }, [path]);

  const listingRootPath = listing.data?.path;
  const listingRef = useRef(listing.data);
  listingRef.current = listing.data;

  useEffect(() => {
    const current = listingRef.current;
    if (!current || listingRootPath === undefined) {
      return;
    }
    const rootNode = listingToDirectoryTreeRoot(current);
    setTreeData([rootNode]);
    setExpandedKeys([rootNode.key]);
    setSelectedPath(rootNode.path);
  }, [listingRootPath]);

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
    } catch (error) {
      if (controller?.signal.aborted) return;
      toast.error({
        message: "读取目录失败",
        description: getErrorMessage(error),
      });
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
    <Stack size={10}>
      <div className="flex flex-wrap items-center gap-2">
        <div className="min-w-[240px] flex-1">
          <Input
            prefix={<Folder className="size-4 text-fg-subtle" />}
            value={selectedPath}
            onChange={(event) => setSelectedPath(event.target.value)}
            onPressEnter={openSelectedPath}
            placeholder="/path/to/downloads"
          />
        </div>
        <Button
          variant="secondary"
          onClick={openSelectedPath}
          disabled={!selectedPath.trim() || listing.isLoading}
        >
          打开
        </Button>
        <Button
          variant="secondary"
          icon={<FolderPlus className="size-3.5" />}
          onClick={createSelectedPath}
          loading={createDirectory.isPending}
          disabled={!selectedPath.trim()}
        >
          创建
        </Button>
        <Button
          variant="primary"
          onClick={() => onSelect(selectedPath.trim())}
          disabled={!selectedPath.trim()}
        >
          选择此目录
        </Button>
      </div>

      <Card size="sm" bodyClassName="p-3">
        <Stack size={8}>
          <Toolbar>
            <Button
              size="sm"
              variant="secondary"
              icon={<ArrowUp className="size-3.5" />}
              disabled={!listing.data?.parent || listing.isLoading}
              onClick={() => listing.data?.parent && setRootPath(listing.data.parent)}
            >
              上级目录
            </Button>
            <EllipsisText
              title={resolvedPath}
              className="flex-1 text-xs font-mono text-fg-muted px-2 min-w-0"
            >
              {resolvedPath}
            </EllipsisText>
            <Tooltip content="刷新目录">
              <Button
                size="sm"
                variant="ghost"
                circle
                icon={<RefreshCw className={`size-3.5 ${listing.isFetching ? "animate-spin" : ""}`} />}
                onClick={() => listing.refetch()}
                aria-label="刷新目录"
              />
            </Tooltip>
          </Toolbar>

          {listing.isError && (
            <Alert
              type="error"
              message="读取目录失败"
              description={listing.error.message}
            />
          )}

          <div className="relative min-h-[140px] rounded-control border border-line bg-surface p-1">
            {listing.isLoading && treeData.length === 0 ? (
              <div className="flex h-32 items-center justify-center">
                <Spinner className="size-5" />
              </div>
            ) : treeData.length > 0 ? (
              <Tree
                nodes={treeData}
                expandedKeys={expandedKeys}
                selectedKey={selectedPath}
                onExpandedChange={setExpandedKeys}
                onSelect={(node) => setSelectedPath((node as DirectoryTreeNode).path)}
                loadChildren={loadDirectoryNode}
              />
            ) : (
              <AppEmpty description="没有子目录" />
            )}
          </div>
        </Stack>
      </Card>
    </Stack>
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
