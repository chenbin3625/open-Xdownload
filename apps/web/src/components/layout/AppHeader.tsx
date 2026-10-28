import { Menu, RefreshCw, Search, WifiOff } from "lucide-react";
import React, { useState } from "react";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Tooltip } from "../ui/Overlay";
import { Tag } from "../ui/Tag";

export interface AppHeaderProps {
  refreshPending: boolean;
  onRefresh: () => void;
  onQuickSubmit: (input: string) => void;
  onToggleMobileMenu?: () => void;
  showMenuButton?: boolean;
  /** SSE 断开时置真：顶部显示"实时更新已断开"，点击可手动重连。 */
  realtimeDisconnected?: boolean;
  onReconnectRealtime?: () => void;
}

export function AppHeader({
  refreshPending,
  onRefresh,
  onQuickSubmit,
  onToggleMobileMenu,
  showMenuButton = false,
  realtimeDisconnected = false,
  onReconnectRealtime,
}: AppHeaderProps) {
  const [quickInput, setQuickInput] = useState("");

  const handleSubmit = () => {
    const trimmed = quickInput.trim();
    if (!trimmed) return;
    onQuickSubmit(trimmed);
    setQuickInput("");
  };

  return (
    <header className="app-header sticky top-0 z-20 shrink-0 border-b border-line/80 glass-header shadow-2xs">
      <div className="flex items-center gap-2 px-3 py-2 sm:h-15 sm:gap-3 sm:px-4 sm:py-0 md:px-6">
        {showMenuButton && (
          <Button
            variant="text"
            icon={<Menu className="size-4" />}
            onClick={onToggleMobileMenu}
            aria-label="打开导航菜单"
            className="hidden sm:inline-flex"
          />
        )}

        <div className="min-w-0 flex-1 md:max-w-140">
          <div className="relative flex items-center group">
            <Input
              value={quickInput}
              onChange={(event) => setQuickInput(event.target.value)}
              onPressEnter={handleSubmit}
              placeholder="粘贴链接、@用户名或列表 ID"
              aria-label="快速解析输入"
              enterKeyHint="go"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              containerClassName="w-full"
              className="bg-surface/90 hover:bg-surface focus:bg-surface transition-all shadow-xs"
              prefix={<Search className="size-3.5 text-fg-subtle group-hover:text-brand-500 transition-colors" />}
              suffix={
                <div className="flex items-center gap-1.5">
                  <Button
                    variant="primary"
                    size="sm"
                    disabled={!quickInput.trim()}
                    onClick={handleSubmit}
                    className="h-9 shadow-xs sm:h-7"
                  >
                    解析
                  </Button>
                </div>
              }
            />
          </div>
        </div>

        {/* 右侧运行状态与操作 */}
        <div className="ml-auto hidden shrink-0 items-center gap-2 sm:flex">
          {realtimeDisconnected && (
            <button
              type="button"
              onClick={onReconnectRealtime}
              title="实时更新已断开，点击重连"
              className="flex min-h-11 cursor-pointer items-center gap-1 rounded-control border border-warning/40 bg-warning-soft px-2 py-1 text-[11px] font-medium text-warning transition-colors hover:border-warning sm:min-h-0"
            >
              <WifiOff className="size-3" />
              <span className="hidden sm:inline">实时更新已断开</span>
              <RefreshCw className="size-3" />
            </button>
          )}

          <Tag tone="brand" className="hidden font-mono text-[11px] shadow-2xs sm:inline-flex">
            v{__APP_VERSION__}
          </Tag>

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
