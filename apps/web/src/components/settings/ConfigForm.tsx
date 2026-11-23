import { t, useTranslation } from "../../lib/i18n";
import {
  Download,
  HardDrive,
  RefreshCw,
  Save,
  Users,
} from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useMemo, useRef, useState } from "react";
import {
  configQueryRoot,
  updateConfig,
  type AppConfig,
} from "../../lib/api";
import { notifyError } from "../common/CommonUI";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { Popconfirm, Tooltip } from "../semi/overlays";
import { Tag } from "../semi/data";
import { toast } from "../semi/toast";
import { XAccountsSettings } from "./XAccountsSettings";
import { readEnvOverridden } from "./EnvOverrideNote";
import { DownloadSettingsFields } from "./DownloadSettings";
import { StorageManagement } from "./StorageManagement";
import { LanguageSettings } from "./LanguageSettings";
import { AccountSecurity } from "./AccountSecurity";

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
  const [draftBase, setDraftBase] = useState(() => normalizeConfig(config));
  const [draft, setDraft] = useState(() => draftBase);
  const draftDirty = useMemo(
    () => configSyncKey(normalizeConfig(draft)) !== configSyncKey(normalizeConfig(draftBase)),
    [draft, draftBase],
  );
  // GET/PUT 响应可能带 envOverridden: string[]（camelCase 字段名），
  // 用于在对应输入框下标注"由环境变量提供"。字段缺失时按空数组处理。
  const envOverridden = readEnvOverridden(config);
  const latestConfigRef = useRef(config);
  latestConfigRef.current = config;
  // 保存成功后记录当时 props 里的旧快照键，直到真正收到不同的服务端新数据时才解除屏蔽；
  // 避免 refetch 缓慢或失败时盲目回滚表单到保存前的旧 props。
  const ignoredPreSaveKeyRef = useRef<string | null>(null);
  const draftRevision = useRef(0);

  useEffect(() => {
    const normalized = normalizeConfig(config);
    const configKey = configSyncKey(normalized);
    if (ignoredPreSaveKeyRef.current !== null) {
      if (configKey === ignoredPreSaveKeyRef.current) {
        return;
      }
      ignoredPreSaveKeyRef.current = null;
    }
    if (!draftDirty) {
      setDraftBase(normalized);
      setDraft(normalized);
    }
  }, [config, draftDirty]);

  useEffect(() => {
    if (!draftDirty) return;
    const protectDraft = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    const protectNavigation = (event: Event) => event.preventDefault();
    window.addEventListener("beforeunload", protectDraft);
    window.addEventListener("settings:before-navigate", protectNavigation);
    return () => { window.removeEventListener("beforeunload", protectDraft); window.removeEventListener("settings:before-navigate", protectNavigation); };
  }, [draftDirty]);

  function updateDraft(action: React.SetStateAction<AppConfig>) {
    draftRevision.current += 1;
    setDraft(action);
  }

  const mutation = useMutation({
    mutationFn: updateConfig,
    onMutate: () => ({ revision: draftRevision.current, draft }),
    onSuccess: (updated, _submitted, submitted) => {
      const normalized = normalizeConfig(updated);
      const currentPropKey = configSyncKey(normalizeConfig(latestConfigRef.current));
      const savedKey = configSyncKey(normalized);
      setDraftBase(normalized);
      if (submitted?.revision === draftRevision.current) {
        ignoredPreSaveKeyRef.current = savedKey !== currentPropKey ? currentPropKey : null;
        setDraft(normalized);
      } else if (submitted) {
        ignoredPreSaveKeyRef.current = savedKey !== currentPropKey ? currentPropKey : null;
        setDraft(current => ({ ...normalized, ...configDiff(submitted.draft, current) }));
      }
      void queryClient.invalidateQueries({ queryKey: configQueryRoot });
      onRefresh?.();
      toast.success({ message: t("配置已保存") });
    },
    onError: notifyError(t("保存失败")),
  });

  const saveHandlerRef = useRef<() => void>(() => {});
  saveHandlerRef.current = () => {
    if (!draftDirty || mutation.isPending) return;
    mutation.mutate(configDiff(draftBase, draft));
  };

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s") {
        if (!draftDirty || mutation.isPending) return;
        event.preventDefault();
        saveHandlerRef.current();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [draftDirty, mutation.isPending]);

  // 重新加载：丢弃草稿并拉取服务器最新配置。
  // dirty 时由 Popconfirm 二次确认（见下方按钮），避免误点丢掉未保存的修改。
  function reloadConfig() {
    draftRevision.current += 1;
    ignoredPreSaveKeyRef.current = null;
    const normalized = normalizeConfig(config);
    setDraftBase(normalized);
    setDraft(normalized);
    onRefresh?.();
  }

  const reloadButton = (
    <Button
      type="tertiary"
      icon={<RefreshCw className={`size-4 ${refreshPending ? "animate-spin" : ""}`} />}
      loading={refreshPending}
      disabled={refreshPending}
      aria-busy={refreshPending}
      aria-label={t("重新加载配置")}
    />
  );

  return (
    <div className="workspace-page settings-form settings-flat">
      <div className="page-heading">
        <h1 className="page-title">{t("系统配置")}</h1>
        <div className="page-actions">
          {draftDirty && <Tag tone="warning">{t("未保存")}</Tag>}
          {draftDirty ? (
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
                icon={<RefreshCw className={`size-4 ${refreshPending ? "animate-spin" : ""}`} />}
                loading={refreshPending}
                disabled={refreshPending}
                aria-busy={refreshPending}
                onClick={reloadConfig}
                aria-label={t("重新加载配置")}
              />
            </Tooltip>
          )}
          <Button
            theme="solid"
            icon={<Save className="size-4" />}
            loading={mutation.isPending}
            disabled={mutation.isPending}
            aria-busy={mutation.isPending}
            onClick={() => mutation.mutate(configDiff(draftBase, draft))}
          >
            {t("保存")}</Button>
        </div>
      </div>

      <nav className="settings-jump-nav" aria-label={t("快速跳转")}>
        <a href="#settings-download" className="settings-jump-link"><Download size={14} />{t("下载与归档")}</a>
        <a href="#settings-storage" className="settings-jump-link"><HardDrive size={14} />{t("存储设置")}</a>
        <a href="#settings-x-accounts" className="settings-jump-link"><Users size={14} />{t("X 账号池")}</a>
      </nav>

      <div className="settings-general-bar">
        <LanguageSettings />
        <AccountSecurity compact />
      </div>

      <section id="settings-download" className="semi-settings-section settings-download">
        <h2 className="settings-section-title"><Download size={16} className="shrink-0" />{t("下载与归档")}</h2>
        <DownloadSettingsFields draft={draft} onChange={updateDraft} envOverridden={envOverridden} />
      </section>

      <StorageManagement />

      <XAccountsSettings />
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

/** 只返回 draft 相对于 base 有变化的字段（PATCH 语义），避免多标签页并发保存时
 *  后提交者把前提交者改的字段覆盖回旧值。 */
function configDiff(base: AppConfig, draft: AppConfig): Partial<AppConfig> {
  const diff: Record<string, unknown> = {};
  for (const key of Object.keys(draft) as (keyof AppConfig)[]) {
    if (JSON.stringify(draft[key]) !== JSON.stringify(base[key])) {
      diff[key] = draft[key];
    }
  }
  return diff as Partial<AppConfig>;
}
