import { t, useTranslation } from "../../lib/i18n";
import {
  Download,
  HardDrive,
  RefreshCw,
  Save,
  ShieldCheck,
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
import { getErrorMessage, notifyError } from "../common/CommonUI";
import { Button } from "@douyinfe/semi-ui";
import { Popconfirm, Tooltip } from "../semi";
import { toast } from "../semi";
import { CookieSettingsFields } from "./CookieSettings";
import { readEnvOverridden } from "./EnvOverrideNote";
import { DownloadSettingsFields } from "./DownloadSettings";
import { StorageSettings } from "./StorageSettings";
import { LanguageSettings } from "./LanguageSettings";

export function ConfigForm({
  config,
  onRefresh,
  refreshPending = false,
}: {
  config: AppConfig;
  onRefresh?: () => void;
  refreshPending?: boolean;
}) {
  useTranslation();
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
      toast.success({ message: t("配置已保存") });
    },
    onError: notifyError(t("保存失败")),
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
          message: t("Cookie 检测通过"),
          description: result.screenName ? `@${result.screenName}` : result.message,
        });
        return;
      }
      toast.warning({
        message: t("Cookie 检测未通过"),
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
          message: t("Cookie 检测失败"),
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
      type="tertiary"
      icon={<RefreshCw className={`size-3.5 ${refreshPending ? "animate-spin" : ""}`} />}
      loading={refreshPending}
      disabled={refreshPending}
      aria-busy={refreshPending}
      aria-label={t("重新加载配置")}
    />
  );

  return (
    <div className="workspace-page">
      <div className="page-heading">
        <h1 className="page-title">{t("系统与存储配置")}</h1>

        <div className="page-actions">
          {draftDirty ? (
            // 有未保存修改：二次确认后再丢弃
            <Popconfirm
              title={t("放弃未保存的修改？")}
              description={t("将重新加载服务器上的最新配置，当前编辑内容会丢失。")}
              okText={t("重新加载")}
              cancelText={t("取消")}
              onConfirm={reloadConfig}
            >
              {reloadButton}
            </Popconfirm>
          ) : (
            <Tooltip content={t("重新加载配置")}>
              <Button
                type="tertiary"
                icon={<RefreshCw className={`size-3.5 ${refreshPending ? "animate-spin" : ""}`} />}
                loading={refreshPending}
                disabled={refreshPending}
                aria-busy={refreshPending}
                onClick={reloadConfig}
                aria-label={t("重新加载配置")}
              />
            </Tooltip>
          )}
          <Button
            type="tertiary"
            icon={<ShieldCheck className="size-4" />}
            loading={authChecking}
            disabled={authChecking}
            aria-busy={authChecking}
            onClick={() => void runAuthCheck(draft, true)}
          >
            {t("检测 Cookie")}</Button>
          <Button
            theme="solid"
            icon={<Save className="size-4" />}
            loading={mutation.isPending}
            disabled={mutation.isPending}
            aria-busy={mutation.isPending}
            onClick={() => mutation.mutate(configDiff(normalizeConfig(config), draft))}
          >
            {t("保存配置")}</Button>
        </div>
      </div>

      <LanguageSettings />

      <section className="semi-settings-section">
        <h2 className="settings-section-title"><HardDrive size={16} />{t("存储设置")}</h2>
        <StorageSettings draft={draft} onChange={updateDraft} envOverridden={envOverridden} />
      </section>

      <section className="semi-settings-section">
        <h2 className="settings-section-title"><Download size={16} />{t("网络与下载调度")}</h2>
        <DownloadSettingsFields
          draft={draft}
          onChange={updateDraft}
          onAuthChange={updateAuthDraft}
          envOverridden={envOverridden}
        />
      </section>

      <section className="semi-settings-section">
        <h2 className="settings-section-title"><ShieldCheck size={16} />{t("X / Twitter Cookie 凭证")}</h2>
        <CookieSettingsFields
          authError={authError}
          authResult={authResult}
          checking={authChecking}
          draft={draft}
          onChange={updateAuthDraft}
          envOverridden={envOverridden}
        />
      </section>
    </div>
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

/** 只返回 draft 相对于 base 有变化的字段（PATCH 语义），避免多标签页并发保存时
 *  后提交者把前提交者改的字段覆盖回旧值。 */
function configDiff(base: AppConfig, draft: AppConfig): Partial<AppConfig> {
  const diff: Record<string, unknown> = {};
  for (const key of Object.keys(draft) as (keyof AppConfig)[]) {
    if (JSON.stringify(draft[key]) !== JSON.stringify(base[key])) {
      diff[key] = draft[key];
    }
  }
  // 后端要求至少有一个字段，空 diff 说明无变化，仍发全量避免 400。
  if (Object.keys(diff).length === 0) return draft;
  return diff as Partial<AppConfig>;
}
