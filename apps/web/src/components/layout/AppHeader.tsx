import { t, useTranslation } from "../../lib/i18n";
import { ArrowRight, Bell, Menu, RefreshCw, Search, TriangleAlert, WifiOff } from "lucide-react";
import React, { useState } from "react";
import { Badge, Button, Input, Popover, Tag, Tooltip } from "@douyinfe/semi-ui";

export interface AppHeaderProps {
  refreshPending: boolean;
  onRefresh: () => void;
  onQuickSubmit: (input: string) => void;
  failedTweetCount: number;
  onOpenFailedDrawer: () => void;
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
  failedTweetCount,
  onOpenFailedDrawer,
  onToggleMobileMenu,
  showMenuButton = false,
  realtimeDisconnected = false,
  onReconnectRealtime,
}: AppHeaderProps) {
  useTranslation();
  const [quickInput, setQuickInput] = useState("");
  const [notificationsOpen, setNotificationsOpen] = useState(false);

  const handleSubmit = () => {
    const trimmed = quickInput.trim();
    if (!trimmed) return;
    onQuickSubmit(trimmed);
    setQuickInput("");
  };

  return (
    <header className="app-header sticky top-0 z-20 shrink-0 border-b border-line bg-surface">
      <div className="header-inner">
        <div className="header-brand">
          <img src="/icon.svg" alt="" className="size-6" />
          <span>open-Xdownload</span>
        </div>
        {showMenuButton && (
          <span className="hidden md:inline-flex">
            <Button
              theme="borderless"
              type="tertiary"
              icon={<Menu className="size-4" />}
              onClick={onToggleMobileMenu}
              aria-label={t("打开导航菜单")}
            />
          </span>
        )}

        <div className="header-quick-submit">
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
            icon={<ArrowRight size={16} />}
            iconPosition="right"
            disabled={!quickInput.trim()}
            onClick={handleSubmit}
          >
            {t("解析")}
          </Button>
        </div>

        {/* 右侧运行状态与操作 */}
        <div className="header-actions">
          <div className="flex items-center gap-1 sm:gap-2">
            {realtimeDisconnected && (
              <Button
                theme="light"
                type="warning"
                onClick={onReconnectRealtime}
                title={t("实时更新已断开，点击重连")}
                aria-label={t("实时更新已断开，点击重连")}
                icon={<WifiOff size={16} />}
              >
                <span className="hidden lg:inline">{t("实时更新已断开")}</span>
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

          <Popover
            trigger="click"
            motion={false}
            position="bottomRight"
            visible={notificationsOpen}
            onVisibleChange={setNotificationsOpen}
            content={
              <section aria-label={t("通知中心")} style={{ width: "min(20rem, calc(100vw - 1.5rem))" }}>
                <div className="border-b border-line px-4 py-3 text-sm font-semibold text-fg">
                  {t("通知中心")}
                </div>
                {failedTweetCount > 0 ? (
                  <div className="flex items-start gap-3 p-4">
                    <TriangleAlert className="mt-0.5 size-4 shrink-0 text-danger" />
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-medium text-fg">{t("失败队列")}</div>
                      <p className="mt-1 mb-3 text-xs text-fg-muted">
                        {t("{{count}} 条待处理", { count: failedTweetCount })}
                      </p>
                      <Button type="danger" onClick={() => {
                        setNotificationsOpen(false);
                        onOpenFailedDrawer();
                      }}>
                        {t("处理失败")}
                      </Button>
                    </div>
                  </div>
                ) : (
                  <p className="px-4 py-8 text-center text-sm text-fg-muted">{t("暂无通知")}</p>
                )}
              </section>
            }
          >
            <Button
              theme="borderless"
              type="tertiary"
              icon={<Badge count={failedTweetCount > 0 ? failedTweetCount : undefined} overflowCount={99} type="danger"><Bell className="size-4" /></Badge>}
              aria-label={t("通知中心")}
              aria-expanded={notificationsOpen}
              title={t("通知中心")}
              onClick={() => setNotificationsOpen((open) => !open)}
            />
          </Popover>
        </div>
      </div>
    </header>
  );
}
