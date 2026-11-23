import { t, useTranslation } from "../../lib/i18n";
import { ArrowUp, Folder, FolderPlus, RefreshCw } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useRef, useState } from "react";
import {
  createLocalDirectory,
  listLocalDirectories,
  type LocalDirectoryListing,
} from "../../lib/api";
import { getErrorMessage, notifyError } from "../common/CommonUI";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Card from "@douyinfe/semi-ui/lib/es/card";
import Empty from "@douyinfe/semi-ui/lib/es/empty";
import Typography from "@douyinfe/semi-ui/lib/es/typography";
import { Alert, Spinner } from "../semi/feedback";
import { Input } from "../semi/controls";
import { Tooltip } from "../semi/overlays";
import { toast } from "../semi/toast";
import { Tree, type TreeNode } from "../semi/Tree";

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
  const queryClient = useQueryClient();
  const [rootPath, setRootPath] = useState(path);
  const [selectedPath, setSelectedPath] = useState(path);
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const [treeData, setTreeData] = useState<DirectoryTreeNode[]>([]);
  const treeDataRef = useRef<DirectoryTreeNode[]>(treeData);
  treeDataRef.current = treeData;
  const [inlineError, setInlineError] = useState("");
  const nodeLoadControllerRef = useRef<AbortController | null>(null);
  const lastListingPathRef = useRef<string | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const listing = useQuery<LocalDirectoryListing>({
    queryKey: ["local-directories", rootPath],
    queryFn: ({ signal }) => listLocalDirectories(rootPath, signal),
    staleTime: 0,
    refetchOnMount: "always",
  });

  const createDirectory = useMutation({
    mutationFn: createLocalDirectory,
    onSuccess: (data) => {
      void queryClient.invalidateQueries({ queryKey: ["local-directories"] });
      if (!mountedRef.current) return;
      setRootPath(data.path);
      setSelectedPath(data.path);
      onSelect(data.path);
      const rootNode = listingToDirectoryTreeRoot(data);
      lastListingPathRef.current = data.path;
      treeDataRef.current = [rootNode];
      setTreeData([rootNode]);
      setExpandedKeys([rootNode.key]);
      toast.success({ message: t("目录已创建并选择") });
    },
    onError: (error) => {
      if (!mountedRef.current) return;
      notifyError(t("创建目录失败"))(error);
    },
  });

  const resolvedPath = listing.isError ? rootPath : listing.data?.path ?? rootPath;

  useEffect(() => {
    setRootPath(path);
    setSelectedPath(path);
  }, [path]);

  useEffect(() => {
    const current = listing.data;
    if (listing.isError || !current) {
      lastListingPathRef.current = null;
      treeDataRef.current = [];
      setTreeData([]);
      setExpandedKeys([]);
      setInlineError("");
      return;
    }
    const rootNode = listingToDirectoryTreeRoot(current);
    const sameRoot = lastListingPathRef.current === current.path;
    if (sameRoot) {
      const merged = mergeDirectoryTreeNodes(treeDataRef.current, [rootNode]);
      const expandableKeys = collectExpandableLoadedKeys(merged);
      treeDataRef.current = merged;
      setTreeData(merged);
      setExpandedKeys((keys) => keys.filter((key) => expandableKeys.has(key)));
    } else {
      lastListingPathRef.current = current.path;
      treeDataRef.current = [rootNode];
      setTreeData([rootNode]);
      setExpandedKeys([rootNode.key]);
      setSelectedPath(rootNode.path);
    }
    setInlineError("");
  }, [listing.data, listing.isError]);

  useEffect(() => {
    const controller = new AbortController();
    nodeLoadControllerRef.current = controller;
    return () => controller.abort();
  }, [rootPath]);

  async function loadDirectoryNode(node: TreeNode) {
    const dirNode = node as DirectoryTreeNode;
    if (dirNode.children || dirNode.isLeaf) {
      return;
    }
    const controller = nodeLoadControllerRef.current;
    try {
      const childListing = await listLocalDirectories(dirNode.path, controller?.signal);
      if (controller?.signal.aborted || !mountedRef.current) {
        throw new DOMException("Aborted", "AbortError");
      }
      const children = childListing.entries.map(directoryEntryToTreeNode);
      setTreeData((current) => {
        const next = updateDirectoryTreeChildren(current, dirNode.key, children);
        treeDataRef.current = next;
        return next;
      });
      setInlineError("");
    } catch (error) {
      if (!controller?.signal.aborted && mountedRef.current) {
        setInlineError(getErrorMessage(error));
      }
      throw error;
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
            icon={<FolderPlus className="size-4" />}
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
              icon={<ArrowUp className="size-4" />}
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
                icon={<RefreshCw className={`size-4 ${listing.isFetching ? "animate-spin" : ""}`} />}
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

function mergeDirectoryTreeNodes(
  previous: DirectoryTreeNode[],
  fresh: DirectoryTreeNode[],
): DirectoryTreeNode[] {
  const prevByKey = new Map(previous.map((node) => [node.key, node]));
  return fresh.map((node) => {
    const prev = prevByKey.get(node.key);
    if (!prev) return node;
    if (node.children) {
      return {
        ...node,
        children: mergeDirectoryTreeNodes(prev.children ?? [], node.children),
      };
    }
    if (prev.children && !node.isLeaf) {
      return {
        ...node,
        children: prev.children,
      };
    }
    return node;
  });
}

function collectExpandableLoadedKeys(nodes: DirectoryTreeNode[], out = new Set<string>()): Set<string> {
  for (const node of nodes) {
    if (!node.isLeaf && node.children !== undefined) {
      out.add(node.key);
      collectExpandableLoadedKeys(node.children, out);
    }
  }
  return out;
}
