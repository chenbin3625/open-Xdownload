import React from "react";
import type { AppConfig, FileNamingMode } from "../../lib/api";
import { SettingsField, SettingsSwitch } from "./SettingsFields";

export const fileNamingOptions: Array<{ value: FileNamingMode; label: string }> = [
  { value: "user_tweet", label: "用户名 + 用户 ID + 推文" },
  { value: "tweet_text", label: "仅推文" },
];

export const downloadSettingsTips = {
  proxy: "支持 http、https、socks5、socks5h，例如 http://127.0.0.1:7890。如需账号密码，可写成 socks5://user:password@127.0.0.1:1080。用户名或密码里的 @、:、/、% 需要 URL 编码；包含账号密码时会随配置保存在本地。",
  concurrency: "后台同时运行的下载任务数，过高可能触发站点限流或增加远程存储压力。",
  maxFilenameLength: "限制保存到磁盘或远程存储的文件名长度，长推文文件名会自动截断。",
  fileNaming: "影响新下载文件的命名方式，已下载文件不会被重命名。",
  autoRetryFailed: "批量归档结束后，自动再次处理失败推文队列。",
  autoFollowProtected: "遇到未关注的保护账号时，使用已配置 Cookie 尝试发起关注后再归档。",
  includeNestedTweetMedia: "开启后会把引用或转推中的媒体也纳入单条下载和批量归档；关闭时只处理当前推文本体媒体。",
};

export function DownloadSettingsFields({
  draft,
  onChange,
  onAuthChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  onAuthChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <div className="settings-field-grid">
      <SettingsField label="代理" hint={downloadSettingsTips.proxy}>
        <input
          className="parser-input"
          value={draft.proxyUrl}
          placeholder="http://127.0.0.1:7890"
          onChange={(event) => onAuthChange((current) => ({ ...current, proxyUrl: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="并发" hint={downloadSettingsTips.concurrency}>
        <input
          className="parser-input"
          type="number"
          min={1}
          max={64}
          value={draft.maxConcurrency}
          onChange={(event) => onChange((current) => ({ ...current, maxConcurrency: Number(event.target.value) || 1 }))}
        />
      </SettingsField>
      <SettingsField label="最大文件名长度" hint={downloadSettingsTips.maxFilenameLength}>
        <input
          className="parser-input"
          type="number"
          min={16}
          max={240}
          value={draft.maxFilenameLength}
          onChange={(event) =>
            onChange((current) => ({ ...current, maxFilenameLength: Number(event.target.value) || 120 }))
          }
        />
      </SettingsField>
      <SettingsField label="文件名命名" hint={downloadSettingsTips.fileNaming}>
        <select
          className="parser-input"
          value={draft.fileNamingMode}
          onChange={(event) => onChange((current) => ({ ...current, fileNamingMode: event.target.value as FileNamingMode }))}
        >
          {fileNamingOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </SettingsField>
      <SettingsSwitch
        label="失败重试"
        hint={downloadSettingsTips.autoRetryFailed}
        checked={draft.autoRetryFailed}
        onChange={(autoRetryFailed) => onChange((current) => ({ ...current, autoRetryFailed }))}
      />
      <SettingsSwitch
        label="保护账号自动关注"
        hint={downloadSettingsTips.autoFollowProtected}
        checked={draft.autoFollowProtected}
        onChange={(autoFollowProtected) => onChange((current) => ({ ...current, autoFollowProtected }))}
      />
      <SettingsSwitch
        label="引用/转推媒体"
        hint={downloadSettingsTips.includeNestedTweetMedia}
        checked={draft.includeNestedTweetMedia}
        onChange={(includeNestedTweetMedia) => onChange((current) => ({ ...current, includeNestedTweetMedia }))}
      />
    </div>
  );
}
