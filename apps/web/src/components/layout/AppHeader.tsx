import { Menu, Moon, RefreshCw, Search, Sun, Zap } from "lucide-react";
import React, { useState } from "react";
import { cn } from "../../lib/cn";
import { useTheme } from "../../lib/useTheme";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Tooltip } from "../ui/Overlay";
import { StatusDot, Tag } from "../ui/Tag";

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
  const { resolved, toggle } = useTheme();

  const handleSubmit = () => {
    const trimmed = quickInput.trim();
    if (!trimmed) return;
    onQuickSubmit(trimmed);
    setQuickInput("");
  };

  return (
    <header className="sticky top-0 z-20 shrink-0 border-b border-line bg-surface">
      <div className="flex h-15 items-center justify-between gap-3 px-4">
        {showMenuButton && (
          <Button
            variant="text"
            icon={<Menu className="size-4" />}
            onClick={onToggleMobileMenu}
            aria-label="打开导航菜单"
          />
        )}

        {/* 快速解析输入 */}
        <div className="min-w-0 flex-1 md:max-w-140">
          <Input
            value={quickInput}
            onChange={(event) => setQuickInput(event.target.value)}
            onPressEnter={handleSubmit}
            placeholder="粘贴 X 推文链接、@用户名或列表 ID，回车快速解析"
            aria-label="快速解析输入"
            prefix={<Search className="size-3.5" />}
            suffix={
              <Button
                variant="primary"
                size="sm"
                disabled={!quickInput.trim()}
                onClick={handleSubmit}
              >
                解析
              </Button>
            }
          />
        </div>

        {/* 右侧运行状态与操作 */}
        <div className="flex shrink-0 items-center gap-2">
          <Tag
            tone={sseConnected ? "success" : "warning"}
            className="hidden items-center gap-1.5 lg:inline-flex"
          >
            <StatusDot tone={sseConnected ? "success" : "warning"} pulse={sseConnected} />
            {sseConnected ? "实时连接" : "正在重连"}
          </Tag>

          <Tag className="hidden items-center gap-1.5 sm:inline-flex">
            <Zap className={cn("size-3.5", activeCount > 0 ? "text-warning" : "text-fg-subtle")} />
            <span className="font-mono">
              并发 {activeCount} / {maxConcurrency}
            </span>
          </Tag>

          <Tooltip title={resolved === "dark" ? "切换到浅色模式" : "切换到深色模式"}>
            <Button
              variant="text"
              onClick={toggle}
              aria-label="切换主题"
              icon={
                resolved === "dark" ? <Sun className="size-4" /> : <Moon className="size-4" />
              }
            />
          </Tooltip>

          <Tooltip title="刷新数据">
            <Button
              icon={<RefreshCw className="size-4" />}
              loading={refreshPending}
              onClick={onRefresh}
              aria-label="刷新数据"
            />
          </Tooltip>
        </div>
      </div>
    </header>
  );
}
