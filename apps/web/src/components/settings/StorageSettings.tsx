import { useMutation } from "@tanstack/react-query";
import React, { useEffect, useState } from "react";
import {
  testStorage,
  type AppConfig,
  type StorageTestResult,
  type StorageType,
} from "../../lib/api";
import { getErrorMessage } from "../../lib/format";
import { toast } from "../../lib/toast";
import { LocalDirectoryPicker } from "./LocalDirectoryPicker";
import { SMBStorageFields } from "./SMBStorageFields";
import { WebDAVStorageFields } from "./WebDAVStorageFields";

export const storageOptions: Array<{ value: StorageType; label: string }> = [
  { value: "local", label: "本地目录" },
  { value: "smb", label: "SMB" },
  { value: "webdav", label: "WebDAV" },
];

export function storageTargetLabel(config: AppConfig) {
  if (config.storageType === "smb") {
    return config.smbShare ? `//${config.smbHost || "SMB"}/${config.smbShare}` : config.smbHost || "SMB 未配置";
  }
  if (config.storageType === "webdav") {
    return config.webdavUrl || "WebDAV 未配置";
  }
  return config.downloadDir || "本地目录未配置";
}
export function StorageSettings({
  draft,
  onChange,
}: {
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  const [testResult, setTestResult] = useState<StorageTestResult | null>(null);
  const [testError, setTestError] = useState("");
  const testable = draft.storageType === "smb" || draft.storageType === "webdav";
  const storageTestMutation = useMutation({
    mutationFn: testStorage,
    onSuccess: (result) => {
      setTestResult(result);
      setTestError("");
      toast("存储测试通过", { description: result.root });
    },
    onError: (error) => {
      const message = getErrorMessage(error);
      setTestResult(null);
      setTestError(message);
      toast("存储测试失败", { description: message, tone: "err" });
    },
  });

  useEffect(() => {
    setTestResult(null);
    setTestError("");
  }, [draft.storageType]);

  return (
    <div className="settings-stack">
      <div className="batch-tabs" role="tablist" aria-label="存储类型">
        {storageOptions.map((option) => (
          <button
            key={option.value}
            type="button"
            role="tab"
            aria-selected={draft.storageType === option.value}
            className={draft.storageType === option.value ? "batch-tab is-active" : "batch-tab"}
            onClick={() => onChange((current) => ({ ...current, storageType: option.value }))}
          >
            {option.label}
          </button>
        ))}
      </div>

      {draft.storageType === "local" ? (
        <LocalDirectoryPicker
          path={draft.downloadDir}
          onSelect={(downloadDir) => onChange((current) => ({ ...current, downloadDir }))}
        />
      ) : null}
      {draft.storageType === "smb" ? <SMBStorageFields draft={draft} onChange={onChange} /> : null}
      {draft.storageType === "webdav" ? <WebDAVStorageFields draft={draft} onChange={onChange} /> : null}
      {testable ? (
        <RemoteStorageTestPanel
          draft={draft}
          error={testError}
          loading={storageTestMutation.isPending}
          result={testResult}
          onTest={() => storageTestMutation.mutate(draft)}
        />
      ) : null}
    </div>
  );
}

export function RemoteStorageTestPanel({
  draft,
  error,
  loading,
  result,
  onTest,
}: {
  draft: AppConfig;
  error: string;
  loading: boolean;
  result: StorageTestResult | null;
  onTest: () => void;
}) {
  const target = storageTargetLabel(draft);
  return (
    <div className="settings-stack">
      <div className="failed-toolbar">
        <span className="job-ellipsis" title={target}>{target}</span>
        <button type="button" className="job-text-btn" disabled={loading} onClick={onTest}>
          {loading ? "测试中…" : "测试连接"}
        </button>
      </div>
      {result ? (
        <div className="settings-note is-ok">
          <strong>{result.message}</strong>
          <span className="job-ellipsis" title={result.root}>{result.root}</span>
          <span className="job-ellipsis" title={result.path}>{result.path}</span>
        </div>
      ) : null}
      {error ? (
        <div className="settings-note is-err">
          <strong>存储测试失败</strong>
          <span>{error}</span>
        </div>
      ) : null}
    </div>
  );
}
