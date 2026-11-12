import { t, useTranslation } from "../../lib/i18n";
import React from "react";
import type { AppConfig } from "../../lib/api";
import { EnvOverrideNote } from "./EnvOverrideNote";
import { LocalDirectoryPicker } from "./LocalDirectoryPicker";

export function storageTargetLabel(config: AppConfig) {
  return config.downloadDir || t("本地目录未配置");
}

export function StorageSettings({
  draft,
  onChange,
  envOverridden = [],
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  envOverridden?: string[];
}) {
  useTranslation();
  return (
    <div className="flex w-full flex-col gap-4">
      <LocalDirectoryPicker
        path={draft.downloadDir}
        onSelect={(downloadDir) => onChange((current) => ({ ...current, downloadDir }))}
      />
      <EnvOverrideNote show={envOverridden.includes("downloadDir")} />
    </div>
  );
}
