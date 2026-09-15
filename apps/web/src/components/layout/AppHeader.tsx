import { Check, Menu, Moon, Palette, RefreshCw, Search, Sun, Zap } from "lucide-react";
import React, { useState } from "react";
import { cn } from "../../lib/cn";
import { THEME_ACCENTS, useTheme } from "../../lib/useTheme";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { SimplePopover, Tooltip } from "../ui/Overlay";
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
  const { resolved, toggle, accent, setAccent } = useTheme();

  const currentAccent = THEME_ACCENTS.find((a) => a.id === accent) || THEME_ACCENTS[0];

  const handleSubmit = () => {
    const trimmed = quickInput.trim();
    if (!trimmed) return;
    onQuickSubmit(trimmed);
    setQuickInput("");
  };

  return (
    <header className="sticky top-0 z-20 shrink-0 border-b border-line/80 glass-header shadow-2xs">
      <div className="flex h-15 items-center justify-between gap-3 px-4 md:px-6">
        {showMenuButton && (
          <Button
            variant="text"
            icon={<Menu className="size-4" />}
            onClick={onToggleMobileMenu}
            aria-label="打开导航菜单"
          />
        )}

        {/* 全局胶囊万能解析输入框 */}
        <div className="min-w-0 flex-1 md:max-w-140">
          <div className="relative flex items-center group">
            <Input
              value={quickInput}
              onChange={(event) => setQuickInput(event.target.value)}
              onPressEnter={handleSubmit}
              placeholder="粘贴 X 推文链接、@用户名或列表 ID，回车快速解析"
              aria-label="快速解析输入"
              className="bg-surface/90 hover:bg-surface focus:bg-surface transition-all shadow-xs"
              prefix={<Search className="size-3.5 text-fg-subtle group-hover:text-brand-500 transition-colors" />}
              suffix={
                <div className="flex items-center gap-1.5">
                  {!quickInput.trim() && (
                    <kbd className="hidden sm:inline-flex items-center gap-0.5 rounded border border-line bg-surface-muted px-1.5 py-0.5 font-mono text-[10px] text-fg-subtle select-none">
                      ↵ Enter
                    </kbd>
                  )}
                  <Button
                    variant="primary"
                    size="sm"
                    disabled={!quickInput.trim()}
                    onClick={handleSubmit}
                    className="shadow-xs"
                  >
                    解析
                  </Button>
                </div>
              }
            />
          </div>
        </div>

        {/* 右侧运行状态与操作 */}
        <div className="flex shrink-0 items-center gap-2">
          <Tag
            tone={sseConnected ? "success" : "warning"}
            className="hidden items-center gap-1.5 lg:inline-flex shadow-2xs"
          >
            <StatusDot tone={sseConnected ? "success" : "warning"} pulse={sseConnected} />
            {sseConnected ? "实时连接" : "正在重连"}
          </Tag>

          <Tag className="hidden items-center gap-1.5 sm:inline-flex shadow-2xs">
            <Zap className={cn("size-3.5", activeCount > 0 ? "text-warning" : "text-fg-subtle")} />
            <span className="font-mono">
              并发 {activeCount} / {maxConcurrency}
            </span>
          </Tag>

          {/* 主题强调色选择器 */}
          <SimplePopover
            align="end"
            trigger={
              <Button
                variant="text"
                aria-label="选择主题强调色"
                icon={
                  <div className="flex items-center gap-1.5">
                    <span
                      className={cn(
                        "size-3 rounded-full border border-black/10 dark:border-white/20 shadow-xs transition-colors",
                        currentAccent.dotColor,
                      )}
                    />
                    <Palette className="size-3.5 text-fg-muted" />
                  </div>
                }
              />
            }
          >
            <div className="w-52 space-y-2">
              <div className="text-xs font-semibold text-fg px-1">主题强调色</div>
              <div className="space-y-1">
                {THEME_ACCENTS.map((opt) => {
                  const isActive = accent === opt.id;
                  return (
                    <button
                      key={opt.id}
                      type="button"
                      onClick={() => setAccent(opt.id)}
                      className={cn(
                        "flex w-full items-center justify-between rounded-control px-2.5 py-1.5 text-xs transition-colors cursor-pointer",
                        isActive
                          ? "bg-brand-500/10 text-brand-500 font-medium"
                          : "text-fg-body hover:bg-surface-hover hover:text-fg",
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <span
                          className={cn(
                            "size-2.5 rounded-full border border-black/10 dark:border-white/20",
                            opt.dotColor,
                          )}
                        />
                        <span>{opt.name}</span>
                      </div>
                      {isActive && <Check className="size-3 text-brand-500" />}
                    </button>
                  );
                })}
              </div>
            </div>
          </SimplePopover>

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
