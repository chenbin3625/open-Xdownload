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
import { Popconfirm, Tooltip } from "../ui/Overlay";
import { toast } from "../ui/Toast";
import { CookieSettingsFields } from "./CookieSettings";
import { readEnvOverridden } from "./EnvOverrideNote";
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
  // GET/PUT 响应可能带 envOverridden: string[]（camelCase 字段名），
  // 用于在对应输入框下标注"由环境变量提供"。字段缺失时按空数组处理。
  const envOverridden = readEnvOverridden(config);
  const [authResult, setAuthResult] = useState<AuthCheck | null>(null);
  const [authError, setAuthError] = useState("");
  const [authChecking, setAuthChecking] = useState(false);
  const [pendingSavedConfigKey, setPendingSavedConfigKey] = useState("");
  const pendingSavedTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const draftRevision = useRef(0);
  const autoCheckedCookieKey = useRef("");
  const authCheckSequence = useRef(0);
  const currentCookieKey = cookieCheckKey(draft);
  const currentCookieKeyRef = useRef(currentCookieKey);
  currentCookieKeyRef.current = currentCookieKey;

  useEffect(() => {
    const normalized = normalizeConfig(config);
    const configKey = configSyncKey(normalized);
    if (pendingSavedConfigKey) {
      if (pendingSavedConfigKey === configKey) {
        setPendingSavedConfigKey("");
        if (pendingSavedTimer.current) {
          clearTimeout(pendingSavedTimer.current);
          pendingSavedTimer.current = null;
        }
        setDraft(normalized);
      }
      return;
    }
    if (!draftDirty) {
      setDraft(normalized);
    }
  }, [config, draftDirty, pendingSavedConfigKey]);

  useEffect(() => () => {
    if (pendingSavedTimer.current) clearTimeout(pendingSavedTimer.current);
  }, []);

  function updateDraft(action: React.SetStateAction<AppConfig>) {
    draftRevision.current += 1;
    setPendingSavedConfigKey("");
    if (pendingSavedTimer.current) {
      clearTimeout(pendingSavedTimer.current);
      pendingSavedTimer.current = null;
    }
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
    onMutate: () => draftRevision.current,
    onSuccess: (updated, _submitted, submittedRevision) => {
      const normalized = normalizeConfig(updated);
      if (submittedRevision === draftRevision.current) {
        setPendingSavedConfigKey(configSyncKey(normalized));
        // 超时保护：env override 等情况下 GET 值可能永远不匹配 PUT 响应，
        // 3 秒后放弃等待，重新同步最近一次 GET 配置。
        if (pendingSavedTimer.current) clearTimeout(pendingSavedTimer.current);
        pendingSavedTimer.current = setTimeout(() => {
          setPendingSavedConfigKey("");
          pendingSavedTimer.current = null;
        }, 3_000);
        setDraft(normalized);
        setDraftDirty(false);
      }
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

  // 重新加载：丢弃草稿并拉取服务器最新配置。
  // dirty 时由 Popconfirm 二次确认（见下方按钮），避免误点丢掉未保存的修改。
  function reloadConfig() {
    draftRevision.current += 1;
    setPendingSavedConfigKey("");
    if (pendingSavedTimer.current) {
      clearTimeout(pendingSavedTimer.current);
      pendingSavedTimer.current = null;
    }
    setDraftDirty(false);
    setDraft(normalizeConfig(config));
    onRefresh?.();
  }

  const reloadButton = (
    <Button
      variant="secondary"
      circle
      icon={<RefreshCw className={`size-3.5 ${refreshPending ? "animate-spin" : ""}`} />}
      loading={refreshPending}
      aria-label="重新加载配置"
    />
  );

  return (
    <div className="flex flex-col gap-5">
      {/* 页头 + 浮动操作区 */}
      <div className="sticky top-0 z-10 -mx-4 -mt-4 bg-canvas/80 px-4 pt-4 pb-3 backdrop-blur-md border-b border-line flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-bold tracking-tight text-fg">系统与存储配置</h1>
          <p className="mt-0.5 text-xs text-fg-muted">
            存储位置、网络代理与 X Cookie 集中管理，修改后点击右上角统一保存
          </p>
        </div>

        <div className="flex items-center gap-2">
          {draftDirty ? (
            // 有未保存修改：二次确认后再丢弃
            <Popconfirm
              title="放弃未保存的修改？"
              description="将重新加载服务器上的最新配置，当前编辑内容会丢失。"
              okText="重新加载"
              cancelText="取消"
              onConfirm={reloadConfig}
            >
              {reloadButton}
            </Popconfirm>
          ) : (
            <Tooltip content="重新加载配置">
              <Button
                variant="secondary"
                circle
                icon={<RefreshCw className={`size-3.5 ${refreshPending ? "animate-spin" : ""}`} />}
                loading={refreshPending}
                onClick={reloadConfig}
                aria-label="重新加载配置"
              />
            </Tooltip>
          )}
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
        <StorageSettings draft={draft} onChange={updateDraft} envOverridden={envOverridden} />
      </SectionCard>

      <SectionCard
        icon={<Download className="size-4" />}
        title="网络与下载调度"
        description="控制代理连接、文件命名方式与下载相关开关"
      >
        <DownloadSettingsFields
          draft={draft}
          onChange={updateDraft}
          onAuthChange={updateAuthDraft}
          envOverridden={envOverridden}
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
          envOverridden={envOverridden}
        />
      </SectionCard>

      <ThemeSettingsSection />
    </div>
  );
}

function ThemeSettingsSection() {
  const { preference, setPreference, accent, setAccent } = useTheme();

  // 强调色色卡上的对勾前景色：各 accent 的色卡底色亮度不同，
  // 不能统一写 text-white（深色钛金的色卡是近白，白勾等于隐形）。
  // 值取自 index.css 里各 accent 的 --color-brand-500 或色卡的 dark 变体。
  const swatchCheckClass: Record<string, string> = {
    "x-blue": "text-[#09090b]",
    titanium: "text-white dark:text-[#09090b]",
    indigo: "text-white",
    emerald: "text-[#09090b]",
    amber: "text-[#09090b]",
  };

  return (
    <SectionCard
      icon={<Palette className="size-4 text-brand-500" />}
      title="界面外观与个性化主题"
      description="定制系统的明暗视觉风格与强调色，设置即刻生效并持久保存在本地"
    >
      <div className="space-y-4">
        <div role="group" aria-label="明暗主题模式">
          <span className="text-xs font-semibold text-fg">明暗主题模式</span>
          <div className="mt-2 flex flex-wrap gap-2">
            {[
              { id: "system", label: "跟随系统偏好", icon: <Monitor className="size-3.5" /> },
              { id: "light", label: "浅色明亮模式", icon: <Sun className="size-3.5" /> },
              { id: "dark", label: "深色黑曜石模式 (OLED)", icon: <Moon className="size-3.5" /> },
            ].map((mode) => (
              <button
                key={mode.id}
                type="button"
                aria-pressed={preference === mode.id}
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

        <div role="group" aria-label="主题强调色">
          <span className="text-xs font-semibold text-fg">主题强调色 (Theme Accent)</span>
          <div className="mt-2 grid grid-cols-2 gap-2.5 sm:grid-cols-5">
            {THEME_ACCENTS.map((opt) => {
              const active = accent === opt.id;
              return (
                <button
                  key={opt.id}
                  type="button"
                  aria-pressed={active}
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
                      "size-6 rounded-full border border-black/10 dark:border-white/20 shadow-xs flex items-center justify-center",
                      swatchCheckClass[opt.id] ?? "text-white",
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
  // envOverridden 是 GET 响应里的服务端元数据，不属于可保存的配置字段：
  // 丢掉它，避免草稿被整包 PUT 时又回传给后端。
  const { envOverridden: _envOverridden, ...rest } = config as AppConfig & {
    envOverridden?: string[];
  };
  return {
    ...rest,
    maxConcurrency: rest.maxConcurrency || 1,
    includeNestedTweetMedia: rest.includeNestedTweetMedia ?? false,
    incrementalArchive: rest.incrementalArchive ?? false,
    storageType: rest.storageType ?? "local",
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
