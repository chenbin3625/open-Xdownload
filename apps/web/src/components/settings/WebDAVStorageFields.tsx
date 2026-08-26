import React from "react";
import type { AppConfig } from "../../lib/api";
import { SettingsField } from "./SettingsFields";

export const webdavTips = {
  webdavUrl: "WebDAV 服务根地址，例如 https://example.com/dav。",
  webdavPath: "WebDAV 根地址下的保存目录，留空表示保存到根目录。",
  remoteUsername: "远程存储账号用户名，留空时按匿名或服务端默认权限尝试。",
  savedSecret: "敏感字段读取时可能显示为 ********；保持不变或留空不会覆盖已有值。",
};

export function WebDAVStorageFields({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <div className="settings-field-grid">
      <SettingsField label="地址" hint={webdavTips.webdavUrl}>
        <input
          className="parser-input"
          value={draft.webdavUrl}
          placeholder="https://example.com/dav"
          onChange={(event) => onChange((current) => ({ ...current, webdavUrl: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="目录" hint={webdavTips.webdavPath}>
        <input
          className="parser-input"
          value={draft.webdavPath}
          placeholder="x-media"
          onChange={(event) => onChange((current) => ({ ...current, webdavPath: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="用户名" hint={webdavTips.remoteUsername}>
        <input
          className="parser-input"
          value={draft.webdavUsername}
          onChange={(event) => onChange((current) => ({ ...current, webdavUsername: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="密码" hint={webdavTips.savedSecret}>
        <input
          className="parser-input"
          type="password"
          value={draft.webdavPassword ?? ""}
          onChange={(event) => onChange((current) => ({ ...current, webdavPassword: event.target.value }))}
        />
      </SettingsField>
    </div>
  );
}
