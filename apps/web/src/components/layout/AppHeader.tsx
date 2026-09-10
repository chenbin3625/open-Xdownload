import {
  MenuOutlined,
  ReloadOutlined,
  SearchOutlined,
  ThunderboltOutlined,
} from "@ant-design/icons";
import { Badge, Button, Flex, Input, Tag, Tooltip } from "antd";
import React, { useState } from "react";

export interface AppHeaderProps {
  sseConnected: boolean;
  activeCount: number;
  maxConcurrency?: number;
  refreshPending: boolean;
  onRefresh: () => void;
  onQuickSubmit: (input: string) => void;
  onToggleMobileMenu?: () => void;
  showMenuButton?: boolean;
}

export function AppHeader({
  sseConnected,
  activeCount,
  maxConcurrency = 8,
  refreshPending,
  onRefresh,
  onQuickSubmit,
  onToggleMobileMenu,
  showMenuButton = false,
}: AppHeaderProps) {
  const [quickInput, setQuickInput] = useState("");

  const handleSubmit = () => {
    const trimmed = quickInput.trim();
    if (!trimmed) return;
    onQuickSubmit(trimmed);
    setQuickInput("");
  };

  return (
    <header className="app-header shrink-0 sticky top-0 z-20">
      <Flex
        align="center"
        justify="space-between"
        gap={12}
        style={{ height: 60, paddingInline: 16 }}
      >
        {showMenuButton && (
          <Button
            type="text"
            icon={<MenuOutlined />}
            onClick={onToggleMobileMenu}
            aria-label="打开导航菜单"
          />
        )}

        {/* 快速解析输入 */}
        <div style={{ flex: 1, maxWidth: 560, minWidth: 0 }}>
          <Input
            value={quickInput}
            onChange={(event) => setQuickInput(event.target.value)}
            onPressEnter={handleSubmit}
            placeholder="粘贴 X 推文链接、@用户名或列表 ID，回车快速解析"
            prefix={<SearchOutlined style={{ color: "var(--text-subtle)" }} />}
            suffix={
              <Button
                type="primary"
                size="small"
                disabled={!quickInput.trim()}
                onClick={handleSubmit}
              >
                解析
              </Button>
            }
          />
        </div>

        {/* 右侧运行状态与刷新 */}
        <Flex align="center" gap={8} style={{ flexShrink: 0 }}>
          <Tag
            color={sseConnected ? "success" : "warning"}
            className="hidden lg:inline-flex"
            style={{ margin: 0, alignItems: "center", gap: 6 }}
          >
            <Badge status={sseConnected ? "processing" : "warning"} />
            {sseConnected ? "实时连接" : "正在重连"}
          </Tag>

          <Tag
            className="hidden sm:inline-flex"
            style={{ margin: 0, alignItems: "center", gap: 6 }}
          >
            <ThunderboltOutlined
              style={{ color: activeCount > 0 ? "#f59e0b" : "var(--text-subtle)" }}
            />
            <span className="font-mono">
              并发 {activeCount} / {maxConcurrency}
            </span>
          </Tag>

          <Tooltip title="刷新数据">
            <Button
              icon={<ReloadOutlined />}
              loading={refreshPending}
              onClick={onRefresh}
              aria-label="刷新数据"
            />
          </Tooltip>
        </Flex>
      </Flex>
    </header>
  );
}
