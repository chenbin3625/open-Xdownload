import {
  CheckCircleOutlined,
  DatabaseOutlined,
  DownloadOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
} from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Card,
  Divider,
  Flex,
  Form,
  Grid,
  Space,
  Tabs,
  Tooltip,
  Typography,
  notification,
} from "antd";
import React, { useEffect, useRef, useState } from "react";
import {
  checkAuth,
  configQueryRoot,
  updateConfig,
  type AppConfig,
  type AuthCheck,
} from "../../lib/api";
import { getErrorMessage } from "../common/CommonUI";
import { CookieSettingsFields } from "./CookieSettings";
import { DownloadSettingsFields } from "./DownloadSettings";
import { StorageSettings } from "./StorageSettings";

const { Text, Title } = Typography;

export function ConfigForm({
  config,
  onRefresh,
  refreshPending = false,
}: {
  config: AppConfig;
  onRefresh?: () => void;
  refreshPending?: boolean;
}) {
  const screens = Grid.useBreakpoint();
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
      queryClient.invalidateQueries({ queryKey: configQueryRoot });
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

  const sections = [
    {
      key: "storage",
      label: <Space><DatabaseOutlined />存储</Space>,
      children: (
        <ConfigPanel title="存储" description="选择下载文件的保存方式与目标目录">
          <StorageSettings draft={draft} onChange={updateDraft} />
        </ConfigPanel>
      ),
    },
    {
      key: "download",
      label: <Space><DownloadOutlined />下载</Space>,
      children: (
        <ConfigPanel title="下载" description="控制网络请求、任务并发和文件命名">
          <DownloadSettingsFields draft={draft} onChange={updateDraft} onAuthChange={updateAuthDraft} />
        </ConfigPanel>
      ),
    },
    {
      key: "cookie",
      label: <Space><SafetyCertificateOutlined />X Cookie</Space>,
      children: (
        <ConfigPanel title="X Cookie" description="配置用于访问 X / Twitter 的账号认证信息">
          <CookieSettingsFields
            authError={authError}
            authResult={authResult}
            checking={authChecking}
            draft={draft}
            onChange={updateAuthDraft}
          />
        </ConfigPanel>
      ),
    },
  ];

  return (
    <Form layout="vertical">
      <Flex vertical gap={16}>
        <Flex align="center" justify="space-between" gap={16} wrap="wrap">
          <Flex vertical>
            <Title level={3}>配置</Title>
            <Text type="secondary">设置存储、下载规则与 X Cookie</Text>
          </Flex>
          <Space wrap>
            <Tooltip title="重新加载配置">
              <Button
                icon={<ReloadOutlined />}
                loading={refreshPending}
                onClick={onRefresh}
              />
            </Tooltip>
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
        </Flex>

        <Card>
          <Tabs
            items={sections}
            tabPlacement={screens.md ? "start" : "top"}
          />
        </Card>
      </Flex>
    </Form>
  );
}

export function ConfigPanel({
  children,
  description,
  title,
}: {
  children: React.ReactNode;
  description?: string;
  title: string;
}) {
  return (
    <Flex vertical>
      <Title level={4}>{title}</Title>
      {description ? <Text type="secondary">{description}</Text> : null}
      <Divider />
      {children}
    </Flex>
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
