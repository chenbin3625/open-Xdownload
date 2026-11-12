import { t, useTranslation } from "../../lib/i18n";
import { Menu, RefreshCw, Search, WifiOff } from "lucide-react";
import React, { useState } from "react";
import { Button, Input, Tag, Tooltip } from "@douyinfe/semi-ui";

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
  useTranslation();
  const [quickInput, setQuickInput] = useState("");

  const handleSubmit = () => {
    const trimmed = quickInput.trim();
    if (!trimmed) return;
    onQuickSubmit(trimmed);
    setQuickInput("");
  };

  return (
    <header className="app-header sticky top-0 z-20 shrink-0 border-b border-line bg-surface">
      <div className="flex min-h-14 items-center gap-2 px-3 py-3 sm:gap-3 sm:px-4 md:px-6">
        {showMenuButton && (
          <span className="hidden sm:inline-flex">
            <Button
              theme="borderless"
              type="tertiary"
              icon={<Menu className="size-4" />}
              onClick={onToggleMobileMenu}
              aria-label={t("打开导航菜单")}
            />
          </span>
        )}

        <div className="min-w-0 flex-1 md:max-w-140">
          <div className="flex items-center gap-2">
            <Input
              value={quickInput}
              onChange={setQuickInput}
              onEnterPress={(event) => { if (!event.nativeEvent.isComposing) handleSubmit(); }}
              placeholder={t("粘贴链接、@用户名或列表 ID")}
              aria-label={t("快速解析输入")}
              enterKeyHint="go"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              className="min-w-0 flex-1"
              prefix={<Search size={16} />}
            />
                  <Button
                    theme="solid"
                    disabled={!quickInput.trim()}
                    onClick={handleSubmit}
                  >
                    {t("解析")}</Button>
          </div>
        </div>

        {/* 右侧运行状态与操作 */}
        <div className="ml-auto hidden shrink-0 items-center gap-2 sm:flex">
          {realtimeDisconnected && (
            <Button
              theme="light"
              type="warning"
              onClick={onReconnectRealtime}
              title={t("实时更新已断开，点击重连")}
              icon={<WifiOff size={16} />}
            >
              <span className="hidden sm:inline">{t("实时更新已断开")}</span>
            </Button>
          )}

          <Tag color="blue" className="hidden font-mono sm:inline-flex">
            v{__APP_VERSION__}
          </Tag>

          <Tooltip content={t("刷新数据")}>
            <Button
              theme="borderless"
              type="tertiary"
              icon={<RefreshCw className="size-4" />}
              loading={refreshPending}
              disabled={refreshPending}
              aria-busy={refreshPending}
              onClick={onRefresh}
              aria-label={t("刷新数据")}
            />
          </Tooltip>
        </div>
      </div>
    </header>
  );
}
