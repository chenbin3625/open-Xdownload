import { SafetyCertificateOutlined } from "@ant-design/icons";
import { useMutation } from "@tanstack/react-query";
import { Alert, Button, Segmented, notification } from "antd";
import React, { useEffect, useState } from "react";
import {
  testStorage,
  type AppConfig,
  type StorageTestResult,
  type StorageType,
} from "../../lib/api";
import {
  EllipsisText,
  Stack,
  Toolbar,
  getErrorMessage,
} from "../common/CommonUI";
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
      notification.success({
        message: "存储测试通过",
        description: result.root,
      });
    },
    onError: (error) => {
      const message = getErrorMessage(error);
      setTestResult(null);
      setTestError(message);
      notification.error({
        message: "存储测试失败",
        description: message,
      });
    },
  });

  useEffect(() => {
    setTestResult(null);
    setTestError("");
  }, [draft.storageType]);

  return (
    <Stack size={16}>
      <Segmented
        block
        value={draft.storageType}
        options={storageOptions.map((option) => ({ value: option.value, label: option.label }))}
        onChange={(value) => onChange((current) => ({ ...current, storageType: value as StorageType }))}
      />

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
    </Stack>
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
  return (
    <Stack size={8} style={{ marginTop: -6 }}>
      <Toolbar>
        <EllipsisText type="secondary" title={storageTargetLabel(draft)} style={{ flex: "1 1 220px", minWidth: 0 }}>
          {storageTargetLabel(draft)}
        </EllipsisText>
        <Button icon={<SafetyCertificateOutlined />} loading={loading} onClick={onTest}>
          测试连接
        </Button>
      </Toolbar>
      {result ? (
        <Alert
          type="success"
          showIcon
          message={result.message}
          description={
            <Stack size={2}>
              <EllipsisText title={result.root}>{result.root}</EllipsisText>
              <EllipsisText type="secondary" title={result.path}>
                {result.path}
              </EllipsisText>
            </Stack>
          }
        />
      ) : null}
      {error ? <Alert type="error" showIcon message="存储测试失败" description={error} /> : null}
    </Stack>
  );
}
