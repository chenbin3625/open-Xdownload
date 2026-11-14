import { t, useTranslation } from "../../lib/i18n";
import {
  Download,
  RefreshCw,
  Save,
} from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import React, { useEffect, useRef, useState } from "react";
import {
  configQueryRoot,
  updateConfig,
  type AppConfig,
} from "../../lib/api";
import { notifyError } from "../common/CommonUI";
import { Button } from "@douyinfe/semi-ui";
import { Popconfirm, Tag, Tooltip } from "../semi";
import { toast } from "../semi";
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
  const [draft, setDraft] = useState(() => normalizeConfig(config));
  const draftBase = useRef(draft);
  const [draftDirty, setDraftDirty] = useState(false);
  const [section, setSection] = useState("storage");
  // GET/PUT 响应可能带 envOverridden: string[]（camelCase 字段名），
  // 用于在对应输入框下标注"由环境变量提供"。字段缺失时按空数组处理。
  const envOverridden = readEnvOverridden(config);
  const [pendingSavedConfigKey, setPendingSavedConfigKey] = useState("");
  const pendingSavedTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const draftRevision = useRef(0);
  const sectionKeys = ["storage", "download", "accounts", "interface", "security"] as const;

  function changeSection(nextSection: string) {
    if (nextSection === section) return true;
    setSection(nextSection);
    return true;
  }

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
        draftBase.current = normalized;
        setDraft(normalized);
      }
      return;
    }
    if (!draftDirty) {
      draftBase.current = normalized;
      setDraft(normalized);
    }
  }, [config, draftDirty, pendingSavedConfigKey]);

  useEffect(() => () => {
    if (pendingSavedTimer.current) clearTimeout(pendingSavedTimer.current);
  }, []);

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
    setPendingSavedConfigKey("");
    if (pendingSavedTimer.current) {
      clearTimeout(pendingSavedTimer.current);
      pendingSavedTimer.current = null;
    }
    setDraftDirty(true);
    setDraft(action);
  }

  const mutation = useMutation({
    mutationFn: updateConfig,
    onMutate: () => ({ revision: draftRevision.current, draft }),
    onSuccess: (updated, _submitted, submitted) => {
      const normalized = normalizeConfig(updated);
      draftBase.current = normalized;
      if (submitted?.revision === draftRevision.current) {
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
      } else if (submitted) {
        setDraft(current => ({ ...normalized, ...configDiff(submitted.draft, current) }));
      }
      void queryClient.invalidateQueries({ queryKey: configQueryRoot });
      onRefresh?.();
      toast.success({ message: t("配置已保存") });
    },
    onError: notifyError(t("保存失败")),
  });

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
    draftBase.current = normalizeConfig(config);
    setDraft(draftBase.current);
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
    <div className="workspace-page settings-form">
      <div className="page-heading">
        <h1 className="page-title">{t("系统配置")}</h1>

        <div className="page-actions" hidden={section !== "download"}>
          {draftDirty && <Tag tone="warning">{t("未保存")}</Tag>}
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
            theme="solid"
            icon={<Save className="size-4" />}
            loading={mutation.isPending}
            disabled={mutation.isPending}
            aria-busy={mutation.isPending}
            onClick={() => mutation.mutate(configDiff(draftBase.current, draft))}
          >
            {t("保存下载设置")}</Button>
        </div>
      </div>

      <div className="settings-navigation" role="tablist" aria-label={t("设置分类")}>
        {[["storage", "存储"], ["download", "下载与归档"], ["accounts", "X 账号"], ["interface", "界面"], ["security", "账号安全"]].map(([key, label]) => <button key={key} type="button" id={`settings-tab-${key}`} role="tab" aria-selected={section === key} aria-controls={`settings-panel-${key}`} tabIndex={section === key ? 0 : -1} onClick={() => changeSection(key)} onKeyDown={event => {
          let index = sectionKeys.indexOf(key as (typeof sectionKeys)[number]);
          if (event.key === "ArrowRight") index = (index + 1) % sectionKeys.length;
          else if (event.key === "ArrowLeft") index = (index + sectionKeys.length - 1) % sectionKeys.length;
          else if (event.key === "Home") index = 0;
          else if (event.key === "End") index = sectionKeys.length - 1;
          else return;
          event.preventDefault();
          if (changeSection(sectionKeys[index])) document.getElementById(`settings-tab-${sectionKeys[index]}`)?.focus();
        }}>{t(label)}{key === "download" && draftDirty && <span className="settings-dirty-dot" aria-hidden="true" />}</button>)}
      </div>
      <div role="tabpanel" id="settings-panel-storage" aria-labelledby="settings-tab-storage" hidden={section !== "storage"}><StorageManagement /></div>

      <section role="tabpanel" id="settings-panel-download" aria-labelledby="settings-tab-download" hidden={section !== "download"} className="semi-settings-section settings-download">
        <h2 className="settings-section-title"><Download size={16} />{t("下载与归档")}</h2>
        <DownloadSettingsFields
          draft={draft}
          onChange={updateDraft}
          envOverridden={envOverridden}
        />
      </section>

      <div role="tabpanel" id="settings-panel-accounts" aria-labelledby="settings-tab-accounts" hidden={section !== "accounts"}><XAccountsSettings /></div>

      <section role="tabpanel" id="settings-panel-interface" aria-labelledby="settings-tab-interface" hidden={section !== "interface"} className="semi-settings-section"><LanguageSettings /></section>
      <div role="tabpanel" id="settings-panel-security" aria-labelledby="settings-tab-security" hidden={section !== "security"}><AccountSecurity /></div>
      {section === "storage" && <AccountSecurity compact />}
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
