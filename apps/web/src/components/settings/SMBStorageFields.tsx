import React from "react";
import type { AppConfig } from "../../lib/api";
import { SettingsField } from "./SettingsFields";

export const smbTips = {
  smbHost: "SMB 服务器地址，可填写 IP 或主机名，不要包含 smb:// 前缀。",
  smbPort: "SMB 默认端口通常为 445。",
  smbShare: "共享名是服务器上暴露的共享根名称，不是完整路径。",
  smbPath: "共享名下的保存目录，留空表示保存到共享根目录。",
  smbDomain: "多数家庭 NAS 可留空；企业域或工作组环境按实际要求填写。",
  remoteUsername: "远程存储账号用户名，留空时按匿名或服务端默认权限尝试。",
  savedSecret: "敏感字段读取时可能显示为 ********；保持不变或留空不会覆盖已有值。",
};

export function SMBStorageFields({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  return (
    <div className="settings-field-grid">
      <SettingsField label="主机" hint={smbTips.smbHost}>
        <input
          className="parser-input"
          value={draft.smbHost}
          placeholder="192.168.1.10"
          onChange={(event) => onChange((current) => ({ ...current, smbHost: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="端口" hint={smbTips.smbPort}>
        <input
          className="parser-input"
          type="number"
          min={1}
          max={65535}
          value={draft.smbPort}
          onChange={(event) => onChange((current) => ({ ...current, smbPort: Number(event.target.value) || 445 }))}
        />
      </SettingsField>
      <SettingsField label="共享名" hint={smbTips.smbShare}>
        <input
          className="parser-input"
          value={draft.smbShare}
          placeholder="downloads"
          onChange={(event) => onChange((current) => ({ ...current, smbShare: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="目录" hint={smbTips.smbPath}>
        <input
          className="parser-input"
          value={draft.smbPath}
          placeholder="x-media"
          onChange={(event) => onChange((current) => ({ ...current, smbPath: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="域" hint={smbTips.smbDomain}>
        <input
          className="parser-input"
          value={draft.smbDomain}
          onChange={(event) => onChange((current) => ({ ...current, smbDomain: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="用户名" hint={smbTips.remoteUsername}>
        <input
          className="parser-input"
          value={draft.smbUsername}
          onChange={(event) => onChange((current) => ({ ...current, smbUsername: event.target.value }))}
        />
      </SettingsField>
      <SettingsField label="密码" hint={smbTips.savedSecret}>
        <input
          className="parser-input"
          type="password"
          value={draft.smbPassword ?? ""}
          onChange={(event) => onChange((current) => ({ ...current, smbPassword: event.target.value }))}
        />
      </SettingsField>
    </div>
  );
}
