import {
  CheckCircleOutlined,
  DatabaseOutlined,
  DownloadOutlined,
  SafetyCertificateOutlined,
  SettingOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Avatar,
  Button,
  Flex,
  Form,
  Space,
  Typography,
  notification,
} from "antd";
import React, { useEffect, useRef, useState } from "react";
import {
  checkAuth,
  updateConfig,
  type AppConfig,
  type AuthCheck,
} from "../../lib/api";
import { getErrorMessage } from "../common/CommonUI";
import { CookieSettingsFields } from "./CookieSettings";
import { DownloadSettingsFields } from "./DownloadSettings";
import { StorageSettings } from "./StorageSettings";

const { Text } = Typography;

export function ConfigForm({
  config,
  onRefresh,
}: {
  config: AppConfig;
  onRefresh?: () => void;
}) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState(() => normalizeConfig(config));
  const [draftDirty, setDraftDirty] = useState(false);
  const [authResult, setAuthResult] = useState<AuthCheck | null>(null);
  const [authError, setAuthError] = useState("");
  const [authChecking, setAuthChecking] = useState(false);
  const pendingSavedConfigKey = useRef("");
  const autoCheckedCookieKey = useRef("");
  const authCheckSequence = useRef(0);
  const currentCookieKey = cookieCheckKey(draft);
  const currentCookieKeyRef = useRef(currentCookieKey);
  currentCookieKeyRef.current = currentCookieKey;

  useEffect(() => {
    const normalized = normalizeConfig(config);
    const configKey = configSyncKey(normalized);
    if (pendingSavedConfigKey.current) {
      if (pendingSavedConfigKey.current === configKey) {
        pendingSavedConfigKey.current = "";
        setDraft(normalized);
      }
      return;
    }
    if (!draftDirty) {
      setDraft(normalized);
    }
  }, [config, draftDirty]);

  function updateDraft(action: React.SetStateAction<AppConfig>) {
    pendingSavedConfigKey.current = "";
    setDraftDirty(true);
    setDraft(action);
  }

  function updateAuthDraft(action: React.SetStateAction<AppConfig>) {
    authCheckSequence.current += 1;
    setAuthResult(null);
    setAuthError("");
    setAuthChecking(false);
    updateDraft(action);
  }

  const mutation = useMutation({
    mutationFn: updateConfig,
    onSuccess: (updated) => {
      const normalized = normalizeConfig(updated);
      pendingSavedConfigKey.current = configSyncKey(normalized);
      setDraft(normalized);
      setDraftDirty(false);
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      onRefresh?.();
      notification.success({ message: "配置已保存" });
    },
    onError: (error) => {
      notification.error({
        message: "保存失败",
        description: getErrorMessage(error),
      });
    },
  });

  async function runAuthCheck(submitted: AppConfig, notify: boolean) {
    const sequence = authCheckSequence.current + 1;
    const cookieKey = cookieCheckKey(submitted);
    authCheckSequence.current = sequence;
    setAuthChecking(true);
    setAuthError("");
    try {
      const result = await checkAuth(submitted);
      if (sequence !== authCheckSequence.current || cookieKey !== currentCookieKeyRef.current) {
        return;
      }
      setAuthResult(result);
      if (!notify) {
        return;
      }
      if (result.ok) {
        notification.success({
          message: "Cookie 检测通过",
          description: result.screenName ? `@${result.screenName}` : result.message,
        });
        return;
      }
      notification.warning({
        message: "Cookie 检测未通过",
        description: result.message,
      });
    } catch (error) {
      if (sequence !== authCheckSequence.current || cookieKey !== currentCookieKeyRef.current) {
        return;
      }
      const message = getErrorMessage(error);
      setAuthResult(null);
      setAuthError(message);
      if (notify) {
        notification.error({
          message: "Cookie 检测失败",
          description: message,
        });
      }
    } finally {
      if (sequence === authCheckSequence.current) {
        setAuthChecking(false);
      }
    }
  }

  useEffect(() => {
    const normalized = normalizeConfig(config);
    const cookieKey = cookieCheckKey(normalized);
    if (autoCheckedCookieKey.current === cookieKey) {
      return;
    }
    autoCheckedCookieKey.current = cookieKey;
    void runAuthCheck(normalized, false);
  }, [config]);

  return (
    <Form layout="vertical" className="config-form">
      <div className="settings-command-bar">
        <div className="settings-heading">
          <Avatar className="settings-heading-icon" icon={<SettingOutlined />} />
          <div className="settings-heading-copy">
            <Text strong>下载配置</Text>
            <Text type="secondary">当前运行参数</Text>
          </div>
        </div>
        <Space wrap className="settings-actions">
          <Button
            icon={<SafetyCertificateOutlined />}
            loading={authChecking}
            onClick={() => void runAuthCheck(draft, true)}
          >
            检测 Cookie
          </Button>
          <Button
            type="primary"
            icon={<CheckCircleOutlined />}
            loading={mutation.isPending}
            onClick={() => mutation.mutate(draft)}
          >
            保存配置
          </Button>
        </Space>
      </div>

      <div className="settings-main">
        <ConfigPanel kind="storage" icon={<DatabaseOutlined />} title="存储">
          <StorageSettings draft={draft} onChange={updateDraft} />
        </ConfigPanel>

        <ConfigPanel kind="download" icon={<DownloadOutlined />} title="下载">
          <DownloadSettingsFields draft={draft} onChange={updateDraft} onAuthChange={updateAuthDraft} />
        </ConfigPanel>

        <ConfigPanel
          kind="cookie"
          icon={<SafetyCertificateOutlined />}
          title="X Cookie"
        >
          <CookieSettingsFields
            authError={authError}
            authResult={authResult}
            checking={authChecking}
            draft={draft}
            onChange={updateAuthDraft}
          />
        </ConfigPanel>
      </div>
    </Form>
  );
}

export function ConfigPanel({
  children,
  description,
  extra,
  icon,
  kind,
  title,
}: {
  children: React.ReactNode;
  description?: string;
  extra?: React.ReactNode;
  icon: React.ReactNode;
  kind: "storage" | "download" | "cookie";
  title: string;
}) {
  return (
    <section className={`settings-panel settings-panel-${kind}`}>
      <Flex align="flex-start" justify="space-between" gap={12} wrap="wrap" className="settings-panel-header">
        <Space align="start" size={10}>
          <span className="settings-panel-icon">{icon}</span>
          <span className="settings-panel-title">
            <Text strong>{title}</Text>
            {description ? <Text type="secondary">{description}</Text> : null}
          </span>
        </Space>
        {extra}
      </Flex>
      {children}
    </section>
  );
}

export function normalizeConfig(config: AppConfig): AppConfig {
  return {
    ...config,
    includeNestedTweetMedia: config.includeNestedTweetMedia ?? false,
    storageType: config.storageType ?? "local",
    smbHost: config.smbHost ?? "",
    smbPort: config.smbPort || 445,
    smbShare: config.smbShare ?? "",
    smbPath: config.smbPath ?? "",
    smbDomain: config.smbDomain ?? "",
    smbUsername: config.smbUsername ?? "",
    smbPassword: config.smbPassword ?? "",
    webdavUrl: config.webdavUrl ?? "",
    webdavPath: config.webdavPath ?? "",
    webdavUsername: config.webdavUsername ?? "",
    webdavPassword: config.webdavPassword ?? "",
  };
}

export function configSyncKey(config: AppConfig) {
  return JSON.stringify(config);
}

export function cookieCheckKey(config: AppConfig) {
  return JSON.stringify([
    config.authToken ?? "",
    config.csrfToken ?? "",
    config.additionalCookies ?? "",
    config.proxyUrl ?? "",
  ]);
}
