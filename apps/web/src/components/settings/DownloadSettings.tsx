import { t, useTranslation } from "../../lib/i18n";
import React, { useState } from "react";
import { ChevronDown, ChevronUp, Filter } from "lucide-react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import type { AppConfig } from "../../lib/api";
import { ArchiveFilterFields, countArchiveFilterRules } from "../common/ArchiveFilterFields";
import { Field, Input, Switch } from "../semi/controls";
import { Tag } from "../semi/data";
import { EnvOverrideNote } from "./EnvOverrideNote";

export function DownloadSettingsFields({
  draft,
  onChange,
  envOverridden = [],
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  envOverridden?: string[];
}) {
  useTranslation();
  const [filterOpen, setFilterOpen] = useState(false);
  const filterCount = countArchiveFilterRules(draft.archiveFilter);

  return <div className="settings-download-grid">
    <Field label={t("网络代理 (Proxy)")} className="settings-proxy-field">
      <Input value={draft.proxyUrl} onChange={event => onChange(current => ({ ...current, proxyUrl: event.target.value }))} placeholder={t("http://127.0.0.1:7890 或 socks5://127.0.0.1:1080")} />
      <EnvOverrideNote show={envOverridden.includes("proxyUrl")} />
    </Field>
    <div className="settings-toggle-list">
      {([
        ["autoRetryFailed", "失败推文自动重试", "自动创建独立任务，重试已到重试时间的失败推文。", "切换失败推文自动重试"],
        ["includeNestedTweetMedia", "引用/转推媒体入库", "开启后会把引用或转推中的媒体也纳入单条下载和批量归档；关闭时只处理当前推文本体媒体。", "切换引用/转推媒体入库"],
        ["incrementalArchive", "增量归档模式", "开启后重复归档从上次成功位置继续，节省 X API 配额。", "切换增量归档模式"],
        ["writeMetadataSidecar", "写入元数据 Sidecar 与 XMP", "归档媒体时同步生成 .json 元数据，并向 JPEG/PNG 无损嵌入 XMP 来源链接与作者信息。", "切换写入元数据"],
        ["offlineThumbnails", "离线优先缩略图", "开启后媒体库与推文页优先使用本地缩略图缓存，不访问外部 X 图床，适合离线或纯内网环境。", "切换离线优先缩略图"],
      ] as const).map(([key, label, description, ariaLabel]) => <div className="settings-toggle-row" key={key}><div><span className="text-sm font-semibold text-fg">{t(label)}</span><p className="text-xs text-fg-subtle mt-1">{t(description)}</p></div><Switch checked={draft[key] ?? false} onChange={checked => onChange(current => ({ ...current, [key]: checked }))} ariaLabel={t(ariaLabel)} /></div>)}
      <div className="settings-toggle-row">
        <div>
          <span className="text-sm font-semibold text-fg">{t("保护账号自动关注")}</span>
          <p className="text-xs text-fg-subtle mt-1">{t("遇到受保护账号时自动发送关注请求，需使用已登录的 X 账号。")}</p>
        </div>
        <Switch checked={draft.autoFollowProtected} onChange={checked => onChange(current => ({ ...current, autoFollowProtected: checked }))} ariaLabel={t("切换保护账号自动关注")} />
      </div>
    </div>

    <div className="rounded-card border border-line bg-surface-muted/30 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Button
          size="small"
          theme="borderless"
          type="tertiary"
          icon={<Filter className="size-3.5" />}
          aria-expanded={filterOpen}
          onClick={() => setFilterOpen((v) => !v)}
        >
          <span className="flex items-center gap-1.5">
            <span>{t("全局归档过滤规则")}</span>
            {filterCount > 0 && <Tag tone="brand" size="sm">{filterCount}</Tag>}
            {filterOpen ? <ChevronUp className="size-3.5" /> : <ChevronDown className="size-3.5" />}
          </span>
        </Button>
        {filterCount > 0 && (
          <Button size="small" theme="borderless" type="tertiary" onClick={() => onChange((c) => ({ ...c, archiveFilter: null }))}>
            {t("重置过滤")}
          </Button>
        )}
      </div>
      {filterOpen && (
        <div className="mt-3 border-t border-line pt-3">
          <ArchiveFilterFields
            value={draft.archiveFilter}
            onChange={(archiveFilter) => onChange((current) => ({ ...current, archiveFilter }))}
          />
        </div>
      )}
    </div>
  </div>;
}
