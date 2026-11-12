import { t, useTranslation } from "../../lib/i18n";
import React from "react";
import type { AppConfig } from "../../lib/api";
import { Field, Input, Switch } from "../semi";
import { EnvOverrideNote } from "./EnvOverrideNote";

export function DownloadSettingsFields({
  draft,
  onChange,
  onAuthChange,
  envOverridden = [],
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  onAuthChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  envOverridden?: string[];
}) {
  useTranslation();
  return <div>
    <Field label={t("网络代理 (Proxy)")}>
      <Input value={draft.proxyUrl} onChange={event => onAuthChange(current => ({ ...current, proxyUrl: event.target.value }))} placeholder={t("http://127.0.0.1:7890 或 socks5://127.0.0.1:1080")} />
      <EnvOverrideNote show={envOverridden.includes("proxyUrl")} />
    </Field>
    <div className="settings-toggle-list">
      {([
        ["autoRetryFailed", "失败推文自动重试", "自动创建独立任务，重试已到重试时间的失败推文。", "切换失败推文自动重试"],
        ["includeNestedTweetMedia", "引用/转推媒体入库", "开启后会把引用或转推中的媒体也纳入单条下载和批量归档；关闭时只处理当前推文本体媒体。", "切换引用/转推媒体入库"],
        ["incrementalArchive", "增量归档模式", "开启后重复归档从上次成功位置继续，节省 X API 配额。", "切换增量归档模式"],
      ] as const).map(([key, label, description, ariaLabel]) => <div className="settings-toggle-row" key={key}><div><span className="text-sm font-medium">{t(label)}</span><p className="text-xs text-fg-subtle mt-1">{t(description)}</p></div><Switch checked={draft[key] ?? false} onChange={checked => onChange(current => ({ ...current, [key]: checked }))} ariaLabel={t(ariaLabel)} /></div>)}
    </div>
    <div className="settings-toggle-row"><span className="text-sm font-medium">{t("保护账号自动关注")}</span><Switch checked={draft.autoFollowProtected} onChange={checked => onChange(current => ({ ...current, autoFollowProtected: checked }))} ariaLabel={t("切换保护账号自动关注")} /></div>
  </div>;
}
