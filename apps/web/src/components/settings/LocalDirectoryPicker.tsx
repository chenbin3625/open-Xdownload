import {
  FolderOpenOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Card,
  Input,
  Space,
  Spin,
  Tooltip,
  Tree,
  notification,
} from "antd";
import type { TreeDataNode } from "antd";
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
  fullWidthStyle,
  getErrorMessage,
} from "../common/CommonUI";

const { DirectoryTree } = Tree;

export type DirectoryTreeNode = TreeDataNode & {
  key: string;
  path: string;
  children?: DirectoryTreeNode[];
};

export function LocalDirectoryPicker({ path, onSelect }: { path: string; onSelect: (path: string) => void }) {
  const [rootPath, setRootPath] = useState(path);
  const [selectedPath, setSelectedPath] = useState(path);
  const [expandedKeys, setExpandedKeys] = useState<React.Key[]>([]);
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
      notification.success({ message: "目录已创建并选择" });
    },
    onError: (error) => {
      notification.error({
        message: "创建目录失败",
        description: getErrorMessage(error),
      });
    },
  });
  const resolvedPath = listing.data?.path ?? rootPath;

  useEffect(() => {
    setRootPath(path);
    setSelectedPath(path);
  }, [path]);

  // 只在解析出的根路径变化时重建树：listing.data 每次 refetch 都是新对象，
  // 以它为依赖会丢掉懒加载的子节点、把树重新收起，并覆盖用户正在输入的路径。
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

  // 卸载后不再写状态，请求也随之取消，与 lib/api.ts 里其他调用保持一致。
  useEffect(() => {
    const controller = new AbortController();
    nodeLoadControllerRef.current = controller;
    return () => controller.abort();
  }, []);

  async function loadDirectoryNode(node: DirectoryTreeNode) {
    if (node.children || node.isLeaf) {
      return;
    }
    const controller = nodeLoadControllerRef.current;
    try {
      const childListing = await listLocalDirectories(node.path, controller?.signal);
      if (controller?.signal.aborted) return;
      const children = childListing.entries.map(directoryEntryToTreeNode);
      setTreeData((current) => updateDirectoryTreeChildren(current, node.key, children));
    } catch (error) {
      if (controller?.signal.aborted) return;
      notification.error({
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
      <Space.Compact style={fullWidthStyle}>
        <Input
          prefix={<FolderOpenOutlined />}
          value={selectedPath}
          onChange={(event) => setSelectedPath(event.target.value)}
          onPressEnter={openSelectedPath}
        />
        <Button onClick={openSelectedPath} disabled={!selectedPath.trim() || listing.isLoading}>
          打开
        </Button>
        <Button onClick={createSelectedPath} loading={createDirectory.isPending} disabled={!selectedPath.trim()}>
          创建
        </Button>
        <Button type="primary" onClick={() => onSelect(selectedPath.trim())} disabled={!selectedPath.trim()}>
          选择此目录
        </Button>
      </Space.Compact>

      <Card size="small">
        <Stack size={8}>
          <Toolbar>
            <Button
              icon={<FolderOpenOutlined />}
              disabled={!listing.data?.parent || listing.isLoading}
              onClick={() => listing.data?.parent && setRootPath(listing.data.parent)}
            >
              上级
            </Button>
            <EllipsisText title={resolvedPath} style={{ flex: "1 1 220px", minWidth: 0 }}>
              {resolvedPath}
            </EllipsisText>
            <Tooltip title="刷新目录">
              <Button
                icon={<ReloadOutlined />}
                loading={listing.isFetching}
                onClick={() => listing.refetch()}
              />
            </Tooltip>
          </Toolbar>

          {listing.isError ? (
            <Alert type="error" showIcon message="读取目录失败" description={listing.error.message} />
          ) : null}

          <Spin spinning={listing.isLoading}>
            {treeData.length > 0 ? (
              <DirectoryTree<DirectoryTreeNode>
                blockNode
                height={280}
                expandAction="doubleClick"
                expandedKeys={expandedKeys}
                loadData={(node) => loadDirectoryNode(node as DirectoryTreeNode)}
                selectedKeys={selectedPath ? [selectedPath] : []}
                treeData={treeData}
                onExpand={(keys) => setExpandedKeys([...keys])}
                onSelect={(_, info) => setSelectedPath((info.node as DirectoryTreeNode).path)}
              />
            ) : (
              <AppEmpty description="没有子目录" />
            )}
          </Spin>
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
    icon: <FolderOpenOutlined />,
    children: listing.entries.map(directoryEntryToTreeNode),
    isLeaf: listing.entries.length === 0,
  };
}

export function directoryEntryToTreeNode(entry: LocalDirectoryListing["entries"][number]): DirectoryTreeNode {
  return {
    key: entry.path,
    path: entry.path,
    title: entry.name,
    icon: <FolderOpenOutlined />,
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
