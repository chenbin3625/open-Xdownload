import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  DeleteOutlined,
  ExclamationCircleOutlined,
  KeyOutlined,
  LoadingOutlined,
  PauseCircleOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import {
  Button,
  Col,
  Form,
  Input,
  Row,
  Space,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import React, { useEffect, useState } from "react";
import type { AppConfig, AuthCheck, ClientStatus } from "../../lib/api";
import { Stack } from "../common/CommonUI";

const { Text } = Typography;

export type CookieClientStatus = ClientStatus;

export const cookieSettingsTips = {
  authToken: "X/Twitter 登录 Cookie 中的 auth_token，用于鉴权。",
  csrfToken: "X/Twitter 登录 Cookie 中的 ct0（CSRF Token），与 auth_token 对应。",
  backupCookie: (
    <Space orientation="vertical" size={2}>
      <Text>用于多账号轮询下载，降低单个账号被 Twitter 限流的概率。</Text>
      <Text>支持每行一个账号，格式为 auth_token=xxx; ct0=yyy。</Text>
      <Text>已保存的 Cookie 在此界面会以 ******** 脱敏显示，新增或修改不受影响。</Text>
    </Space>
  ),
};

export type BackupCookieRow = {
  authToken: string;
  csrfToken: string;
};

export function CookieSettingsFields({
  authError,
  authResult,
  checking,
  draft,
  onChange,
}: {
  authError: string;
  authResult: AuthCheck | null;
  checking: boolean;
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
}) {
  const clients = authResult?.diagnostics?.clients ?? [];
  const primaryClient = clients.find((client) => client.primary) ?? clients[0];
  const backupClients = clients.filter((client) => !client.primary);
  const primaryComplete = Boolean((draft.authToken ?? "").trim() && (draft.csrfToken ?? "").trim());
  const sharedStatus = {
    checking,
    checked: Boolean(authResult),
    errorMessage: authError || (authResult && !authResult.diagnostics ? authResult.message : ""),
  };

  return (
    <Stack size={16}>
      <Stack size={8}>
        <Text strong>主 Cookie</Text>
        <Row gutter={[16, 0]}>
          <Col xs={24} lg={12}>
            <Form.Item label="auth_token" tooltip={cookieSettingsTips.authToken}>
              <Input
                aria-label="主 Cookie auth_token"
                prefix={<KeyOutlined />}
                suffix={(
                  <CookieTokenStatus
                    {...sharedStatus}
                    client={primaryClient}
                    hasValue={Boolean((draft.authToken ?? "").trim())}
                    pairComplete={primaryComplete}
                  />
                )}
                value={draft.authToken ?? ""}
                onChange={(event) => onChange((current) => ({ ...current, authToken: event.target.value }))}
                placeholder="输入 auth_token"
              />
            </Form.Item>
          </Col>
          <Col xs={24} lg={12}>
            <Form.Item label="ct0" tooltip={cookieSettingsTips.csrfToken}>
              <Input
                aria-label="主 Cookie ct0"
                prefix={<KeyOutlined />}
                suffix={(
                  <CookieTokenStatus
                    {...sharedStatus}
                    client={primaryClient}
                    hasValue={Boolean((draft.csrfToken ?? "").trim())}
                    pairComplete={primaryComplete}
                  />
                )}
                value={draft.csrfToken ?? ""}
                onChange={(event) => onChange((current) => ({ ...current, csrfToken: event.target.value }))}
                placeholder="输入 ct0"
              />
            </Form.Item>
          </Col>
        </Row>
      </Stack>
      <BackupCookieInputs
        clients={backupClients}
        {...sharedStatus}
        value={draft.additionalCookies ?? ""}
        onChange={(additionalCookies) => onChange((current) => ({ ...current, additionalCookies }))}
      />
    </Stack>
  );
}

export function CookieTokenStatus({
  aggregateClients,
  checked,
  checking,
  client,
  errorMessage,
  hasValue,
  pairComplete,
}: {
  aggregateClients?: CookieClientStatus[];
  checked: boolean;
  checking: boolean;
  client?: CookieClientStatus;
  errorMessage: string;
  hasValue: boolean;
  pairComplete: boolean;
}) {
  let tone = "neutral";
  let label = "待检测";
  let detail = "保存或输入 Cookie 后可进行检测";
  let icon: React.ReactNode = <PauseCircleOutlined />;

  if (!hasValue) {
    label = "待配置";
    detail = "尚未填写此 token";
  } else if (!pairComplete) {
    tone = "warning";
    label = "待补全";
    detail = "auth_token 与 ct0 需要成对填写";
    icon = <ExclamationCircleOutlined />;
  } else if (checking) {
    tone = "checking";
    label = "检测中";
    detail = "正在检查 Cookie 状态";
    icon = <LoadingOutlined spin />;
  } else if (aggregateClients?.length) {
    const available = aggregateClients.filter((item) => item.ok).length;
    const hasTransientError = aggregateClients.some((item) => item.ok && item.error);
    tone = available === aggregateClients.length && !hasTransientError
      ? "success"
      : available === 0
        ? "error"
        : "warning";
    label = `${available}/${aggregateClients.length} 可用`;
    detail = aggregateClients
      .map((item) => cookieClientStatusDetail(item))
      .join("；");
    icon = tone === "success"
      ? <CheckCircleOutlined />
      : tone === "error"
        ? <CloseCircleOutlined />
        : <ExclamationCircleOutlined />;
  } else if (client) {
    if (!client.ok || client.disabled) {
      tone = "error";
      label = "异常";
      icon = <CloseCircleOutlined />;
    } else if (client.error) {
      tone = "warning";
      label = "暂时受限";
      icon = <ExclamationCircleOutlined />;
    } else {
      tone = "success";
      label = "有效";
      icon = <CheckCircleOutlined />;
    }
    detail = cookieClientStatusDetail(client);
  } else if (errorMessage) {
    tone = "error";
    label = "检测失败";
    detail = errorMessage;
    icon = <CloseCircleOutlined />;
  } else if (checked) {
    tone = "neutral";
    label = "未检测";
    detail = "此 Cookie 未进入检测队列，可能未填写完整或与其他 Cookie 重复";
  }

  const color = {
    checking: "processing",
    error: "error",
    success: "success",
    warning: "warning",
  }[tone];

  return (
    <Tooltip title={detail}>
      <Tag color={color} icon={icon} aria-label={detail}>{label}</Tag>
    </Tooltip>
  );
}

export function cookieClientStatusDetail(client: CookieClientStatus) {
  const account = client.screenName ? `@${client.screenName}` : `账号 ${client.index + 1}`;
  if (client.error) {
    return `${account}：${client.error}`;
  }
  return `${account}：Cookie 有效`;
}

export function BackupCookieInputs({
  checked,
  checking,
  clients,
  errorMessage,
  value,
  onChange,
}: {
  checked: boolean;
  checking: boolean;
  clients: CookieClientStatus[];
  errorMessage: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const redactedCookieValue = "********";
  const [rows, setRows] = useState<BackupCookieRow[]>(() => parseBackupCookieRows(value));

  useEffect(() => {
    const nextRows = parseBackupCookieRows(value);
    const nextValue = normalizeBackupCookieRows(nextRows);
    setRows((currentRows) =>
      normalizeBackupCookieRows(currentRows) === nextValue ? currentRows : nextRows,
    );
  }, [value]);

  function updateRow(index: number, field: keyof BackupCookieRow, nextValue: string) {
    setRows((currentRows) => {
      const nextRows = currentRows.map((row, rowIndex) => {
        if (rowIndex !== index) {
          return row;
        }
        if (isRedactedBackupCookieRow(row) && nextValue !== redactedCookieValue) {
          return { authToken: field === "authToken" ? nextValue : "", csrfToken: field === "csrfToken" ? nextValue : "" };
        }
        return { ...row, [field]: nextValue };
      });
      onChange(normalizeBackupCookieRows(nextRows));
      return nextRows;
    });
  }

  function addRow() {
    setRows((currentRows) => {
      const nextRows = [...currentRows, emptyBackupCookieRow()];
      onChange(normalizeBackupCookieRows(nextRows));
      return nextRows;
    });
  }

  function removeRow(index: number) {
    setRows((currentRows) => {
      const nextRows = currentRows.filter((_, rowIndex) => rowIndex !== index);
      const safeRows = nextRows.length > 0 ? nextRows : [emptyBackupCookieRow()];
      onChange(normalizeBackupCookieRows(safeRows));
      return safeRows;
    });
  }

  return (
    <Form.Item
      tooltip={cookieSettingsTips.backupCookie}
      label={
        <Space>
          <Text strong>备用 Cookie</Text>
          <Tooltip title="添加备用 Cookie">
            <Button
              aria-label="添加备用 Cookie"
              size="small"
              type="text"
              icon={<PlusOutlined />}
              onClick={addRow}
            />
          </Tooltip>
        </Space>
      }
    >
      <Stack size={8}>
        {rows.map((row, index) => {
          const pairComplete = Boolean(row.authToken.trim() && row.csrfToken.trim());
          const aggregateClients = isRedactedBackupCookieRow(row) && clients.length > 1 ? clients : undefined;
          const client = aggregateClients ? undefined : clients[index];
          const statusProps = { aggregateClients, checked, checking, client, errorMessage, pairComplete };
          return (
            <Row key={index} gutter={[8, 8]} align="middle">
              <Col xs={24} md={11}>
                <Input
                  aria-label={`备用 Cookie ${index + 1} auth_token`}
                  prefix={<KeyOutlined />}
                  suffix={(
                    <CookieTokenStatus
                      {...statusProps}
                      hasValue={Boolean(row.authToken.trim())}
                    />
                  )}
                  value={row.authToken}
                  onChange={(event) => updateRow(index, "authToken", event.target.value)}
                  placeholder={`备用 ${index + 1} auth_token`}
                />
              </Col>
              <Col xs={24} md={11}>
                <Input
                  aria-label={`备用 Cookie ${index + 1} ct0`}
                  prefix={<KeyOutlined />}
                  suffix={(
                    <CookieTokenStatus
                      {...statusProps}
                      hasValue={Boolean(row.csrfToken.trim())}
                    />
                  )}
                  value={row.csrfToken}
                  onChange={(event) => updateRow(index, "csrfToken", event.target.value)}
                  placeholder={`备用 ${index + 1} ct0`}
                />
              </Col>
              <Col xs={24} md={2}>
                <Tooltip title="删除备用 Cookie">
                  <Button
                    aria-label={`删除备用 Cookie ${index + 1}`}
                    danger
                    type="text"
                    icon={<DeleteOutlined />}
                    onClick={() => removeRow(index)}
                  />
                </Tooltip>
              </Col>
            </Row>
          );
        })}
      </Stack>
    </Form.Item>
  );
}

export function emptyBackupCookieRow(): BackupCookieRow {
  return { authToken: "", csrfToken: "" };
}

export function parseBackupCookieRows(value: string): BackupCookieRow[] {
  const trimmed = value.trim();
  if (!trimmed) {
    return [emptyBackupCookieRow()];
  }
  if (trimmed === "********") {
    return [{ authToken: "********", csrfToken: "********" }];
  }

  const jsonRows = parseBackupCookieRowsFromJSON(trimmed);
  if (jsonRows.length > 0) {
    return jsonRows;
  }

  const rows: BackupCookieRow[] = [];
  let current = emptyBackupCookieRow();
  const flush = () => {
    if (current.authToken || current.csrfToken) {
      rows.push(current);
    }
    current = emptyBackupCookieRow();
  };

  for (const rawLine of trimmed.split(/\r?\n/)) {
    const line = rawLine.trim().replace(/^-+\s*/, "");
    if (!line) {
      flush();
      continue;
    }
    if (line.includes(":") && !/[;,]/.test(line)) {
      if (setBackupCookieValue(current, line) && current.authToken && current.csrfToken) {
        flush();
      }
      continue;
    }
    for (const token of line.split(/[;,\s]+/)) {
      setBackupCookieValue(current, token);
    }
    if (current.authToken && current.csrfToken) {
      flush();
    }
  }
  flush();
  return rows.length > 0 ? rows : [emptyBackupCookieRow()];
}

export function parseBackupCookieRowsFromJSON(value: string): BackupCookieRow[] {
  try {
    const parsed: unknown = JSON.parse(value);
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed.flatMap((item) => {
      if (!item || typeof item !== "object") {
        return [];
      }
      const record = item as Record<string, unknown>;
      const authToken = firstString(record.authToken, record.auth_token);
      const csrfToken = firstString(record.csrfToken, record.ct0);
      return authToken || csrfToken ? [{ authToken, csrfToken }] : [];
    });
  } catch {
    return [];
  }
}

export function setBackupCookieValue(current: BackupCookieRow, raw: string) {
  const [key, value] = splitCookieKeyValue(raw);
  if (!key || !value) {
    return false;
  }
  if (key === "auth_token" || key === "authToken") {
    current.authToken = value;
    return true;
  }
  if (key === "ct0" || key === "csrfToken") {
    current.csrfToken = value;
    return true;
  }
  return false;
}

export function splitCookieKeyValue(raw: string) {
  const separatorIndex = raw.search(/[=:]/);
  if (separatorIndex < 0) {
    return ["", ""] as const;
  }
  const key = raw.slice(0, separatorIndex).trim();
  const value = raw
    .slice(separatorIndex + 1)
    .trim()
    .replace(/^["']|["']$/g, "");
  return [key, value] as const;
}

export function firstString(...values: unknown[]) {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

export function normalizeBackupCookieRows(rows: BackupCookieRow[]) {
  return rows
    .map((row) => ({ authToken: row.authToken.trim(), csrfToken: row.csrfToken.trim() }))
    .filter((row) => Boolean(row.authToken || row.csrfToken))
    .map((row) => [
      row.authToken ? `auth_token=${row.authToken}` : "",
      row.csrfToken ? `ct0=${row.csrfToken}` : "",
    ].filter(Boolean).join("; "))
    .join("\n");
}

export function isRedactedBackupCookieRow(row: BackupCookieRow) {
  return row.authToken === "********" && row.csrfToken === "********";
}
