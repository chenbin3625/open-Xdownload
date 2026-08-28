import {
  ExclamationCircleOutlined,
  MenuOutlined,
  MoonOutlined,
  ReloadOutlined,
  SearchOutlined,
  SunOutlined,
  ThunderboltOutlined,
} from "@ant-design/icons";
import { Badge, Button, Input, Tag, Tooltip } from "antd";
import React, { useState } from "react";
import type { AppTheme } from "../../lib/useTheme";

export interface AppHeaderProps {
  sseConnected: boolean;
  activeCount: number;
  maxConcurrency?: number;
  refreshPending: boolean;
  onRefresh: () => void;
  theme: AppTheme;
  onToggleTheme: () => void;
  onQuickSubmit: (input: string) => void;
  onToggleMobileMenu?: () => void;
}

export function AppHeader({
  sseConnected,
  activeCount,
  maxConcurrency = 8,
  refreshPending,
  onRefresh,
  theme,
  onToggleTheme,
  onQuickSubmit,
  onToggleMobileMenu,
}: AppHeaderProps) {
  const [quickInput, setQuickInput] = useState("");

  const handleSubmit = () => {
    const trimmed = quickInput.trim();
    if (!trimmed) return;
    onQuickSubmit(trimmed);
    setQuickInput("");
  };

  return (
    <header className="h-14 border-b border-slate-200 dark:border-slate-800/80 px-4 md:px-6 flex items-center justify-between gap-4 sticky top-0 bg-white/90 dark:bg-slate-950/90 backdrop-blur-md z-20 transition-colors duration-200 shrink-0 select-none">
      {/* Left Mobile Menu Button */}
      <div className="flex items-center gap-2 md:hidden">
        <Button
          type="text"
          icon={<MenuOutlined className="text-base" />}
          onClick={onToggleMobileMenu}
          className="!h-8 !w-8 !p-0 !flex !items-center !justify-center"
          aria-label="打开导航菜单"
        />
      </div>

      {/* Center Smart Universal Input using Ant Design Input */}
      <div className="flex-1 max-w-2xl">
        <Input
          value={quickInput}
          onChange={(e) => setQuickInput(e.target.value)}
          onPressEnter={handleSubmit}
          placeholder="智能快速解析：输入或粘贴 X 推文链接、@用户名、列表 ID，按回车..."
          prefix={<SearchOutlined className="text-slate-400 text-xs mr-1" />}
          suffix={
            <Button
              type="primary"
              size="small"
              disabled={!quickInput.trim()}
              onClick={handleSubmit}
              className="!h-7 !px-3 !rounded-lg !text-xs !font-medium"
            >
              快速解析
            </Button>
          }
          className="!h-9 !rounded-xl !text-[13px] !bg-slate-100/90 dark:!bg-slate-900 dark:!border-slate-800"
        />
      </div>

      {/* Right Status Badges & Controls using Ant Design Tag, Tooltip & Button */}
      <div className="flex items-center gap-2 shrink-0">
        {/* SSE Status Indicator */}
        <Tag
          color={sseConnected ? "success" : "warning"}
          className="!m-0 !h-8 !hidden lg:!flex !items-center !gap-1.5 !px-2.5 !rounded-lg !text-[11px] !font-medium select-none"
        >
          <Badge status={sseConnected ? "processing" : "warning"} />
          <span>{sseConnected ? "实时连接" : "正在重连"}</span>
        </Tag>

        {/* Active Concurrency Badge */}
        <Tag
          className="!m-0 !h-8 !hidden sm:!flex !items-center !gap-1.5 !px-2.5 !rounded-lg !text-[11px] !font-mono !bg-slate-100 dark:!bg-slate-900 dark:!border-slate-800 dark:!text-slate-300 select-none"
        >
          <ThunderboltOutlined
            className={
              activeCount > 0 ? "text-amber-500 animate-pulse" : "text-slate-400"
            }
          />
          <span>
            并发: {activeCount} / {maxConcurrency}
          </span>
        </Tag>

        {/* Refresh Button */}
        <Tooltip title="刷新数据状态">
          <Button
            type="default"
            icon={
              <ReloadOutlined
                className={`text-xs ${refreshPending ? "animate-spin text-sky-500" : ""}`}
              />
            }
            onClick={onRefresh}
            loading={refreshPending}
            className="!h-8 !w-8 !p-0 !rounded-lg !flex !items-center !justify-center"
            aria-label="刷新数据"
          />
        </Tooltip>

        {/* Theme Toggle Button */}
        <Tooltip title={theme === "dark" ? "切换为明亮模式" : "切换为暗黑模式"}>
          <Button
            type="default"
            icon={
              theme === "dark" ? (
                <SunOutlined className="text-amber-400 text-sm" />
              ) : (
                <MoonOutlined className="text-indigo-600 text-sm" />
              )
            }
            onClick={onToggleTheme}
            className="!h-8 !w-8 !p-0 !rounded-lg !flex !items-center !justify-center"
            aria-label="切换明暗主题"
          />
        </Tooltip>
      </div>
    </header>
  );
}
