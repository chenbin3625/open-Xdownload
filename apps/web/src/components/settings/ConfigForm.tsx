import {
  Check,
  Download,
  HardDrive,
  Monitor,
  Moon,
  Palette,
  RefreshCw,
  Save,
  ShieldCheck,
  Sun,
} from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useRef, useState } from "react";
import {
  checkAuth,
  configQueryRoot,
  updateConfig,
  type AppConfig,
  type AuthCheck,
} from "../../lib/api";
import { THEME_ACCENTS, useTheme } from "../../lib/useTheme";
import { cn } from "../../lib/cn";
import { getErrorMessage, notifyError } from "../common/CommonUI";
import { Button } from "../ui/Button";
import { SectionCard } from "../ui/Card";
import { Tooltip } from "../ui/Overlay";
import { toast } from "../ui/Toast";
import { CookieSettingsFields } from "./CookieSettings";
import { DownloadSettingsFields } from "./DownloadSettings";
import { StorageSettings } from "./StorageSettings";

export function ConfigForm({
  config,
  onRefresh,
  refreshPending = false,
}: {
  config: AppConfig;
  onRefresh?: () => void;
  refreshPending?: boolean;
}) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState(() => normalizeConfig(config));
  const [draftDirty, setDraftDirty] = useState(false);
  const [authResult, setAuthResult] = useState<AuthCheck | null>(null);
  const [authError, setAuthError] = useState("");
  const [authChecking, setAuthChecking] = useState(false);
  const pendingSavedConfigKey = useRef("");
  const autoCheckedCookieKey = useRef("");
  const authCheckSequence = useRef(0);
  const currentCookieKey = cookieCheckKey(draft);
  const currentCookieKeyRef = useRef(currentCookieKey);
  currentCookieKeyRef.current = currentCookieKey;

  useEffect(() => {
    const normalized = normalizeConfig(config);
    const configKey = configSyncKey(normalized);
    if (pendingSavedConfigKey.current) {
      if (pendingSavedConfigKey.current === configKey) {
        pendingSavedConfigKey.current = "";
        setDraft(normalized);
      }
      return;
    }
    if (!draftDirty) {
      setDraft(normalized);
    }
  }, [config, draftDirty]);

  function updateDraft(action: React.SetStateAction<AppConfig>) {
    pendingSavedConfigKey.current = "";
    setDraftDirty(true);
    setDraft(action);
  }

  function updateAuthDraft(action: React.SetStateAction<AppConfig>) {
    authCheckSequence.current += 1;
    setAuthResult(null);
    setAuthError("");
    setAuthChecking(false);
    updateDraft(action);
  }

  const mutation = useMutation({
    mutationFn: updateConfig,
    onSuccess: (updated) => {
      const normalized = normalizeConfig(updated);
      pendingSavedConfigKey.current = configSyncKey(normalized);
      setDraft(normalized);
      setDraftDirty(false);
      void queryClient.invalidateQueries({ queryKey: configQueryRoot });
      onRefresh?.();
      toast.success({ message: "配置已保存" });
    },
    onError: notifyError("保存失败"),
  });

  async function runAuthCheck(submitted: AppConfig, notify: boolean) {
    const sequence = authCheckSequence.current + 1;
    const cookieKey = cookieCheckKey(submitted);
    authCheckSequence.current = sequence;
    setAuthChecking(true);
    setAuthError("");
    try {
      const result = await checkAuth(submitted);
      if (sequence !== authCheckSequence.current || cookieKey !== currentCookieKeyRef.current) {
        return;
      }
      setAuthResult(result);
      if (!notify) {
        return;
      }
      if (result.ok) {
        toast.success({
          message: "Cookie 检测通过",
          description: result.screenName ? `@${result.screenName}` : result.message,
        });
        return;
      }
      toast.warning({
        message: "Cookie 检测未通过",
        description: result.message,
      });
    } catch (error) {
      if (sequence !== authCheckSequence.current || cookieKey !== currentCookieKeyRef.current) {
        return;
      }
      const message = getErrorMessage(error);
      setAuthResult(null);
      setAuthError(message);
      if (notify) {
        toast.error({
          message: "Cookie 检测失败",
          description: message,
        });
      }
    } finally {
      if (sequence === authCheckSequence.current) {
        setAuthChecking(false);
      }
    }
  }

  useEffect(() => {
    const normalized = normalizeConfig(config);
    const cookieKey = cookieCheckKey(normalized);
    if (autoCheckedCookieKey.current === cookieKey) {
      return;
    }
    autoCheckedCookieKey.current = cookieKey;
    void runAuthCheck(normalized, false);
  }, [config]);

  return (
    <div className="flex flex-col gap-5">
      {/* 页头 + 浮动操作区 */}
      <div className="sticky top-0 z-10 -mx-4 -mt-4 bg-canvas/80 px-4 pt-4 pb-3 backdrop-blur-md border-b border-line flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-fg">系统与存储配置</h1>
          <p className="mt-0.5 text-xs text-fg-muted">
            存储位置、下载并发与 X Cookie 集中管理，修改后点击右上角统一保存
          </p>
        </div>

        <div className="flex items-center gap-2">
          <Tooltip content="重新加载配置">
            <Button
              variant="secondary"
              circle
              icon={<RefreshCw className={`size-3.5 ${refreshPending ? "animate-spin" : ""}`} />}
              loading={refreshPending}
              onClick={onRefresh}
              aria-label="重新加载配置"
            />
          </Tooltip>
          <Button
            variant="secondary"
            icon={<ShieldCheck className="size-4" />}
            loading={authChecking}
            onClick={() => void runAuthCheck(draft, true)}
          >
            检测 Cookie
          </Button>
          <Button
            variant="primary"
            icon={<Save className="size-4" />}
            loading={mutation.isPending}
            onClick={() => mutation.mutate(draft)}
          >
            保存配置
          </Button>
        </div>
      </div>

      <SectionCard
        icon={<HardDrive className="size-4" />}
        title="存储设置"
        description="选择媒体下载文件的保存方式与目标目录"
      >
        <StorageSettings draft={draft} onChange={updateDraft} />
      </SectionCard>

      <SectionCard
        icon={<Download className="size-4" />}
        title="网络与下载调度"
        description="控制代理连接、下载并发与文件命名方式"
      >
        <DownloadSettingsFields
          draft={draft}
          onChange={updateDraft}
          onAuthChange={updateAuthDraft}
        />
      </SectionCard>

      <SectionCard
        icon={<ShieldCheck className="size-4" />}
        title="X / Twitter Cookie 凭证"
        description="配置主账号与备用多账号凭证池，用于规避 API 频率限制"
      >
        <CookieSettingsFields
          authError={authError}
          authResult={authResult}
          checking={authChecking}
          draft={draft}
          onChange={updateAuthDraft}
        />
      </SectionCard>

      <ThemeSettingsSection />
    </div>
  );
}

function ThemeSettingsSection() {
  const { preference, setPreference, accent, setAccent } = useTheme();

  return (
    <SectionCard
      icon={<Palette className="size-4 text-brand-500" />}
      title="界面外观与个性化主题"
      description="定制系统的明暗视觉风格与强调色，设置即刻生效并持久保存在本地"
    >
      <div className="space-y-4">
        <div>
          <label className="text-xs font-semibold text-fg">明暗主题模式</label>
          <div className="mt-2 flex flex-wrap gap-2">
            {[
              { id: "system", label: "跟随系统偏好", icon: <Monitor className="size-3.5" /> },
              { id: "light", label: "浅色明亮模式", icon: <Sun className="size-3.5" /> },
              { id: "dark", label: "深色黑曜石模式 (OLED)", icon: <Moon className="size-3.5" /> },
            ].map((mode) => (
              <button
                key={mode.id}
                type="button"
                onClick={() => setPreference(mode.id as any)}
                className={cn(
                  "flex items-center gap-2 rounded-control border px-3.5 py-2 text-xs font-medium cursor-pointer transition-all",
                  preference === mode.id
                    ? "border-brand-500 bg-brand-500/10 text-brand-500 shadow-2xs"
                    : "border-line bg-surface text-fg-body hover:bg-surface-hover hover:border-line-strong",
                )}
              >
                {mode.icon}
                <span>{mode.label}</span>
              </button>
            ))}
          </div>
        </div>

        <div>
          <label className="text-xs font-semibold text-fg">主题强调色 (Theme Accent)</label>
          <div className="mt-2 grid grid-cols-2 gap-2.5 sm:grid-cols-5">
            {THEME_ACCENTS.map((opt) => {
              const active = accent === opt.id;
              return (
                <button
                  key={opt.id}
                  type="button"
                  onClick={() => setAccent(opt.id)}
                  className={cn(
                    "flex flex-col items-center gap-1.5 rounded-control border p-3 text-center cursor-pointer transition-all",
                    active
                      ? "border-brand-500 bg-brand-500/10 shadow-2xs"
                      : "border-line bg-surface hover:bg-surface-hover hover:border-line-strong",
                  )}
                >
                  <span
                    className={cn(
                      "size-6 rounded-full border border-black/10 dark:border-white/20 shadow-xs flex items-center justify-center text-white",
                      opt.dotColor,
                    )}
                  >
                    {active && <Check className="size-3.5 stroke-[3]" />}
                  </span>
                  <span className="text-xs font-semibold text-fg">{opt.name}</span>
                  <span className="text-[10px] text-fg-subtle truncate max-w-full">
                    {opt.description}
                  </span>
                </button>
              );
            })}
          </div>
        </div>
      </div>
    </SectionCard>
  );
}

export function normalizeConfig(config: AppConfig): AppConfig {
  return {
    ...config,
    includeNestedTweetMedia: config.includeNestedTweetMedia ?? false,
    incrementalArchive: config.incrementalArchive ?? false,
    storageType: config.storageType ?? "local",
  };
}

export function configSyncKey(config: AppConfig) {
  return JSON.stringify(config);
}

export function cookieCheckKey(config: AppConfig) {
  return JSON.stringify([
    config.authToken ?? "",
    config.csrfToken ?? "",
    config.additionalCookies ?? "",
    config.proxyUrl ?? "",
  ]);
}
