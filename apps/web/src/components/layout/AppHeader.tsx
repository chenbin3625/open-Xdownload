import { t, useTranslation } from "../../lib/i18n";
import { ArrowRight, Bell, Menu, Monitor, Moon, RefreshCw, Search, Sun, TriangleAlert, WifiOff } from "lucide-react";
import React, { useEffect, useMemo, useRef, useState } from "react";
import Badge from "@douyinfe/semi-ui/lib/es/badge";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Popover from "@douyinfe/semi-ui/lib/es/popover";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import { detectQuickInputTab } from "../../lib/quickInput";
import { readThemePreference, setThemePreference, subscribeTheme, type ThemePreference } from "../../lib/theme";

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

function isEditableElement(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

export const AppHeader = React.memo(function AppHeader({
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
  const [themePref, setThemePref] = useState<ThemePreference>(() => readThemePreference());
  const quickInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => subscribeTheme(setThemePref), []);

  const cycleTheme = () => {
    const order: ThemePreference[] = ["system", "light", "dark"];
    const next = order[(order.indexOf(themePref) + 1) % order.length];
    setThemePreference(next);
  };
  const themeLabel = themePref === "dark" ? t("深色模式") : themePref === "light" ? t("浅色模式") : t("跟随系统");
  const themeIcon = themePref === "dark" ? <Moon className="size-4" /> : themePref === "light" ? <Sun className="size-4" /> : <Monitor className="size-4" />;

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.isComposing || event.defaultPrevented) return;
      // 有弹窗或抽屉打开时不抢夺全局快捷键
      if (document.querySelector('[role="dialog"], [role="alertdialog"]')) return;
      const isCmdK = (event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k";
      const isSlash = event.key === "/" && !event.metaKey && !event.ctrlKey && !event.altKey && !isEditableElement(event.target);
      if (isCmdK || isSlash) {
        event.preventDefault();
        quickInputRef.current?.focus();
        quickInputRef.current?.select();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, []);

  const trimmedInput = quickInput.trim();
  const detectedKind = useMemo(
    () => (trimmedInput ? detectQuickInputTab(trimmedInput) : null),
    [trimmedInput],
  );
  const detectedLabel =
    detectedKind === "tweet_link"
      ? t("单条推文")
      : detectedKind === "list"
        ? t("列表归档")
        : detectedKind === "user"
          ? t("用户归档")
          : "";

  const handleSubmit = () => {
    if (!trimmedInput) return;
    onQuickSubmit(trimmedInput);
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
            ref={quickInputRef}
            value={quickInput}
            onChange={setQuickInput}
            onEnterPress={(event) => { if (!event.nativeEvent.isComposing) handleSubmit(); }}
            onKeyDown={(event) => {
              if (event.key === "Escape" && !event.nativeEvent.isComposing) {
                event.currentTarget.blur();
              }
            }}
            placeholder={t("粘贴链接、@用户名或列表 ID")}
            aria-label={t("快速解析输入")}
            enterKeyHint="go"
            autoCapitalize="none"
            autoCorrect="off"
            spellCheck={false}
            showClear
            className="min-w-0 flex-1"
            prefix={<Search size={16} />}
            suffix={
              trimmedInput ? (
                <span className="header-quick-suffix hidden sm:inline-flex" title={t("识别为：{{value0}}", { value0: detectedLabel })}>
                  <Tag size="small" color={detectedKind === "tweet_link" ? "amber" : detectedKind === "list" ? "indigo" : "blue"}>
                    {detectedLabel}
                  </Tag>
                </span>
              ) : (
                <span className="header-quick-suffix hidden sm:inline-flex" title={t("快捷键 ⌘K 或 / 聚焦")} aria-hidden="true">
                  <kbd className="kbd-hint">/</kbd>
                </span>
              )
            }
          />
          <Button
            theme={trimmedInput ? "solid" : "light"}
            icon={<ArrowRight size={16} />}
            iconPosition="right"
            disabled={!trimmedInput}
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

            <Tooltip content={`${t("切换外观")}: ${themeLabel}`}>
              <Button
                theme="borderless"
                type="tertiary"
                icon={themeIcon}
                onClick={cycleTheme}
                aria-label={`${t("切换外观")}: ${themeLabel}`}
              />
            </Tooltip>

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
                      <div className="text-sm font-semibold text-fg">{t("失败队列")}</div>
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
});
