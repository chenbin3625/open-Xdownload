import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useRef, useState } from "react";
import {
  checkAuth,
  configQueryRoot,
  updateConfig,
  type AppConfig,
  type AuthCheck,
} from "../../lib/api";
import { getErrorMessage } from "../../lib/format";
import { toast } from "../../lib/toast";
import { CookieSettingsFields } from "./CookieSettings";
import { DownloadSettingsFields } from "./DownloadSettings";
import { StorageSettings } from "./StorageSettings";

export function ConfigForm({
  config,
  onRefresh,
}: {
  config: AppConfig;
  onRefresh?: () => void;
}) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState(() => normalizeConfig(config));
  const [draftDirty, setDraftDirty] = useState(false);
  const [authResult, setAuthResult] = useState<AuthCheck | null>(null);
  const [authError, setAuthError] = useState("");
  const [authChecking, setAuthChecking] = useState(false);
  const pendingSavedConfigKey = useRef("");
  const pendingSavedConfigTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const autoCheckedCookieKey = useRef("");
  const authCheckSequence = useRef(0);
  const currentCookieKey = cookieCheckKey(draft);
  const currentCookieKeyRef = useRef(currentCookieKey);
  currentCookieKeyRef.current = currentCookieKey;

  // draft 与远端 config 的同步策略（F1）：
  //  - draftDirty=true 表示用户在编辑中，外部 config 更新（如 SSE 兜底刷新）不应覆盖表单；
  //  - 保存成功后，onSuccess 设置 pendingSavedConfigKey 记录本次保存的归一化键，等下一个
  //    config 批次到达时如果匹配该键，则用服务器返回值替换 draft 并清除标记；
  //  - 其余情况（未在编辑、无待处理保存）config 变化时直接跟随。
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

  useEffect(() => () => {
    if (pendingSavedConfigTimer.current) {
      clearTimeout(pendingSavedConfigTimer.current);
    }
  }, []);

  function updateDraft(action: React.SetStateAction<AppConfig>) {
    pendingSavedConfigKey.current = "";
    if (pendingSavedConfigTimer.current) {
      clearTimeout(pendingSavedConfigTimer.current);
      pendingSavedConfigTimer.current = null;
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
    onSuccess: (updated) => {
      const normalized = normalizeConfig(updated);
      pendingSavedConfigKey.current = configSyncKey(normalized);
      if (pendingSavedConfigTimer.current) {
        clearTimeout(pendingSavedConfigTimer.current);
      }
      pendingSavedConfigTimer.current = setTimeout(() => {
        pendingSavedConfigKey.current = "";
        pendingSavedConfigTimer.current = null;
      }, 5000);
      setDraft(normalized);
      setDraftDirty(false);
      queryClient.setQueryData(configQueryRoot, updated);
      onRefresh?.();
      toast("配置已保存");
    },
    onError: (error) => {
      toast("保存失败", { description: getErrorMessage(error), tone: "err" });
    },
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
        toast("Cookie 检测通过", { description: result.screenName ? `@${result.screenName}` : result.message });
        return;
      }
      toast("Cookie 检测未通过", { description: result.message, tone: "err" });
    } catch (error) {
      if (sequence !== authCheckSequence.current || cookieKey !== currentCookieKeyRef.current) {
        return;
      }
      const message = getErrorMessage(error);
      setAuthResult(null);
      setAuthError(message);
      if (notify) {
        toast("Cookie 检测失败", { description: message, tone: "err" });
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
    <form
      className="config-form"
      onSubmit={(event) => {
        event.preventDefault();
        mutation.mutate(draft);
      }}
    >
      <div className="settings-command-bar">
        <div className="settings-heading">
          <span className="settings-heading-icon" aria-hidden="true">⚙</span>
          <div className="settings-heading-copy">
            <strong>下载配置</strong>
            <span>当前运行参数</span>
          </div>
        </div>
        <div className="settings-actions">
          <button
            type="button"
            className="job-text-btn"
            disabled={authChecking}
            onClick={() => void runAuthCheck(draft, true)}
          >
            {authChecking ? "检测中…" : "检测 Cookie"}
          </button>
          <button type="submit" className="shell-primary-btn" disabled={mutation.isPending}>
            {mutation.isPending ? "保存中…" : "保存配置"}
          </button>
        </div>
      </div>

      <div className="settings-main">
        <ConfigPanel kind="storage" title="存储">
          <StorageSettings draft={draft} onChange={updateDraft} />
        </ConfigPanel>
        <ConfigPanel kind="download" title="下载">
          <DownloadSettingsFields draft={draft} onChange={updateDraft} onAuthChange={updateAuthDraft} />
        </ConfigPanel>
        <ConfigPanel kind="cookie" title="X Cookie">
          <CookieSettingsFields
            authError={authError}
            authResult={authResult}
            checking={authChecking}
            draft={draft}
            onChange={updateAuthDraft}
          />
        </ConfigPanel>
      </div>
    </form>
  );
}
export function ConfigPanel({
  children,
  description,
  extra,
  kind,
  title,
}: {
  children: React.ReactNode;
  description?: string;
  extra?: React.ReactNode;
  kind: "storage" | "download" | "cookie";
  title: string;
}) {
  return (
    <section className={`settings-panel settings-panel-${kind}`}>
      <div className="settings-panel-header">
        <div className="settings-panel-heading">
          <span className="settings-panel-icon" aria-hidden="true">
            {kind === "storage" ? "▣" : kind === "download" ? "↓" : "⌘"}
          </span>
          <span className="settings-panel-title">
            <strong>{title}</strong>
            {description ? <span>{description}</span> : null}
          </span>
        </div>
        {extra}
      </div>
      {children}
    </section>
  );
}

export function normalizeConfig(config: AppConfig): AppConfig {
  return {
    ...config,
    includeNestedTweetMedia: config.includeNestedTweetMedia ?? false,
    storageType: config.storageType ?? "local",
    smbHost: config.smbHost ?? "",
    smbPort: config.smbPort || 445,
    smbShare: config.smbShare ?? "",
    smbPath: config.smbPath ?? "",
    smbDomain: config.smbDomain ?? "",
    smbUsername: config.smbUsername ?? "",
    smbPassword: config.smbPassword ?? "",
    webdavUrl: config.webdavUrl ?? "",
    webdavPath: config.webdavPath ?? "",
    webdavUsername: config.webdavUsername ?? "",
    webdavPassword: config.webdavPassword ?? "",
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
