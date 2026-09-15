import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  HelpCircle,
  Key,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import React, { useEffect, useState } from "react";
import type { AppConfig, AuthCheck, ClientStatus } from "../../lib/api";
import { Button } from "../ui/Button";
import { Alert } from "../ui/Feedback";
import { Field, PasswordInput } from "../ui/Input";
import { Tooltip } from "../ui/Overlay";
import { Tag, type Tone } from "../ui/Tag";

export type CookieClientStatus = ClientStatus;

export const cookieHelpSteps = [
  "在浏览器中登录 x.com，建议使用独立浏览器配置或无痕窗口，避免影响日常登录状态。",
  "打开开发者工具：F12，或 macOS 上 Cmd+Option+I、Windows / Linux 上 Ctrl+Shift+I。",
  "切换到「应用 / Application」面板（Firefox 为「存储 / Storage」），左侧展开「Cookies」并选择 https://x.com。",
  "复制 auth_token 一行的 Value 填入上方 auth_token，再复制 ct0 一行的 Value 填入 ct0；两者必须来自同一个浏览器、同一个账号。",
  "保存配置后点击「检测 Cookie」，确认状态显示为有效。",
];

export const cookieHelpNotes = [
  "auth_token 是 HttpOnly Cookie，控制台执行 document.cookie 读不到，必须用开发者工具的 Cookies 面板复制。",
  "退出登录、修改密码或 X 主动失效会话后 Cookie 会失效，按同样步骤重新复制即可。",
  "一个浏览器配置只保留一个 X 登录态；备用 Cookie 可用无痕窗口或另一个浏览器配置登录其他账号后获取。",
  "Cookie 等同于账号密码，只会保存在本地数据库中，请勿分享或粘贴到其他站点。",
];

export const cookieSettingsTips = {
  authToken: "X/Twitter 登录 Cookie 中的 auth_token，用于鉴权。位置：开发者工具「应用 → Cookies → https://x.com」，需与 ct0 来自同一账号。",
  csrfToken: "X/Twitter 登录 Cookie 中的 ct0（CSRF Token），与 auth_token 对应。位置同上，取 Cookies 面板中 ct0 一行的 Value。",
  backupCookie: (
    <div className="space-y-1 text-xs">
      <div>用于多账号轮询下载，降低单个账号被 Twitter 限流的概率。</div>
      <div>支持每行一个账号，格式为 auth_token=xxx; ct0=yyy。</div>
      <div>已保存的 Cookie 在此界面会以 ******** 脱敏显示，新增或修改不受影响。</div>
    </div>
  ),
};

function TipIcon({ content }: { content: React.ReactNode }) {
  return (
    <Tooltip content={content}>
      <span className="inline-flex cursor-help text-fg-subtle hover:text-fg-muted">
        <HelpCircle className="size-3.5" />
      </span>
    </Tooltip>
  );
}

export type BackupCookieRow = {
  id: string;
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
    <div className="space-y-5">
      <CookieHelpAlert />

      {/* 主 Cookie */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <span className="text-sm font-semibold text-fg">主账号 Cookie (Primary)</span>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field
            label="auth_token"
            tooltip={<TipIcon content={cookieSettingsTips.authToken} />}
          >
            <PasswordInput
              aria-label="主 Cookie auth_token"
              prefix={<Key className="size-3.5 text-fg-subtle" />}
              suffix={
                <CookieTokenStatus
                  {...sharedStatus}
                  client={primaryClient}
                  hasValue={Boolean((draft.authToken ?? "").trim())}
                  pairComplete={primaryComplete}
                />
              }
              value={draft.authToken ?? ""}
              onChange={(event) =>
                onChange((current) => ({ ...current, authToken: event.target.value }))
              }
              placeholder="输入 auth_token"
            />
          </Field>

          <Field
            label="ct0 (CSRF Token)"
            tooltip={<TipIcon content={cookieSettingsTips.csrfToken} />}
          >
            <PasswordInput
              aria-label="主 Cookie ct0"
              prefix={<Key className="size-3.5 text-fg-subtle" />}
              suffix={
                <CookieTokenStatus
                  {...sharedStatus}
                  client={primaryClient}
                  hasValue={Boolean((draft.csrfToken ?? "").trim())}
                  pairComplete={primaryComplete}
                />
              }
              value={draft.csrfToken ?? ""}
              onChange={(event) =>
                onChange((current) => ({ ...current, csrfToken: event.target.value }))
              }
              placeholder="输入 ct0"
            />
          </Field>
        </div>
      </div>

      {/* 备用 Cookie 账号池 */}
      <div className="pt-3 border-t border-line">
        <BackupCookieInputs
          clients={backupClients}
          {...sharedStatus}
          value={draft.additionalCookies ?? ""}
          onChange={(additionalCookies) =>
            onChange((current) => ({ ...current, additionalCookies }))
          }
        />
      </div>
    </div>
  );
}

export function CookieHelpAlert() {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-card border border-info/30 bg-info-soft/60 p-3.5 text-xs text-fg-body">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 font-medium text-fg">
          <HelpCircle className="size-4 text-info" />
          <span>如何获取 X Cookie（auth_token / ct0）？</span>
        </div>
        <button
          type="button"
          onClick={() => setOpen((c) => !c)}
          className="cursor-pointer text-xs font-medium text-brand-600 dark:text-brand-400 hover:underline"
        >
          {open ? "收起指引" : "展开指引"}
        </button>
      </div>

      {open && (
        <div className="mt-3 space-y-2 border-t border-info/20 pt-3">
          <ol className="list-decimal pl-4 space-y-1">
            {cookieHelpSteps.map((step) => (
              <li key={step}>{step}</li>
            ))}
          </ol>
          <div className="space-y-1 text-fg-muted pt-1">
            {cookieHelpNotes.map((note) => (
              <p key={note} className="text-[11px]">
                · {note}
              </p>
            ))}
          </div>
        </div>
      )}
    </div>
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
  let tone: Tone = "neutral";
  let label = "待检测";
  let detail = "保存或输入 Cookie 后可进行检测";
  let icon: React.ReactNode = null;

  if (!hasValue) {
    label = "待配置";
    detail = "尚未填写此 token";
  } else if (!pairComplete) {
    tone = "warning";
    label = "待补全";
    detail = "auth_token 与 ct0 需要成对填写";
    icon = <AlertTriangle className="size-3" />;
  } else if (checking) {
    tone = "brand";
    label = "检测中";
    detail = "正在检查 Cookie 状态";
    icon = <RefreshCw className="size-3 animate-spin" />;
  } else if (aggregateClients?.length) {
    const available = aggregateClients.filter((item) => item.ok).length;
    const hasTransientError = aggregateClients.some((item) => item.ok && item.error);
    tone =
      available === aggregateClients.length && !hasTransientError
        ? "success"
        : available === 0
        ? "danger"
        : "warning";
    label = `${available}/${aggregateClients.length} 可用`;
    detail = aggregateClients.map((item) => cookieClientStatusDetail(item)).join("；");
    icon =
      tone === "success" ? (
        <CheckCircle2 className="size-3" />
      ) : tone === "danger" ? (
        <AlertCircle className="size-3" />
      ) : (
        <AlertTriangle className="size-3" />
      );
  } else if (client) {
    if (!client.ok || client.disabled) {
      tone = "danger";
      label = "异常";
      icon = <AlertCircle className="size-3" />;
    } else if (client.error) {
      tone = "warning";
      label = "暂时受限";
      icon = <AlertTriangle className="size-3" />;
    } else {
      tone = "success";
      label = "有效";
      icon = <CheckCircle2 className="size-3" />;
    }
    detail = cookieClientStatusDetail(client);
  } else if (errorMessage) {
    tone = "danger";
    label = "检测失败";
    detail = errorMessage;
    icon = <AlertCircle className="size-3" />;
  } else if (checked) {
    tone = "neutral";
    label = "未检测";
    detail = "此 Cookie 未进入检测队列，可能未填写完整或与其他 Cookie 重复";
  }

  return (
    <Tooltip content={detail}>
      <span>
        <Tag tone={tone} size="sm" icon={icon} aria-label={detail}>
          {label}
        </Tag>
      </span>
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
          return {
            id: row.id,
            authToken: field === "authToken" ? nextValue : "",
            csrfToken: field === "csrfToken" ? nextValue : "",
          };
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
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-1.5">
          <span className="text-sm font-semibold text-fg">备用 Cookie 多账号池</span>
          <TipIcon content={cookieSettingsTips.backupCookie} />
        </div>
        <Button
          size="sm"
          variant="secondary"
          icon={<Plus className="size-3.5" />}
          onClick={addRow}
        >
          添加备用账号
        </Button>
      </div>

      <div className="space-y-2.5">
        {rows.map((row, index) => {
          const pairComplete = Boolean(row.authToken.trim() && row.csrfToken.trim());
          const aggregateClients =
            isRedactedBackupCookieRow(row) && clients.length > 1 ? clients : undefined;
          const client = aggregateClients ? undefined : clients[index];
          const statusProps = {
            aggregateClients,
            checked,
            checking,
            client,
            errorMessage,
            pairComplete,
          };

          return (
            <div
              key={row.id}
              className="flex flex-col gap-2 rounded-control border border-line bg-surface-muted/40 p-2.5 sm:flex-row sm:items-center"
            >
              <div className="flex-1">
                <PasswordInput
                  aria-label={`备用 Cookie ${index + 1} auth_token`}
                  prefix={<Key className="size-3.5 text-fg-subtle" />}
                  suffix={
                    <CookieTokenStatus
                      {...statusProps}
                      hasValue={Boolean(row.authToken.trim())}
                    />
                  }
                  value={row.authToken}
                  onChange={(event) => updateRow(index, "authToken", event.target.value)}
                  placeholder={`备用 ${index + 1} auth_token`}
                  size="sm"
                />
              </div>

              <div className="flex-1">
                <PasswordInput
                  aria-label={`备用 Cookie ${index + 1} ct0`}
                  prefix={<Key className="size-3.5 text-fg-subtle" />}
                  suffix={
                    <CookieTokenStatus
                      {...statusProps}
                      hasValue={Boolean(row.csrfToken.trim())}
                    />
                  }
                  value={row.csrfToken}
                  onChange={(event) => updateRow(index, "csrfToken", event.target.value)}
                  placeholder={`备用 ${index + 1} ct0`}
                  size="sm"
                />
              </div>

              <div className="flex shrink-0 items-center justify-end">
                <Tooltip content="删除此备用 Cookie">
                  <Button
                    size="sm"
                    variant="ghost"
                    circle
                    icon={<Trash2 className="size-3.5 text-danger" />}
                    onClick={() => removeRow(index)}
                    aria-label={`删除备用 Cookie ${index + 1}`}
                  />
                </Tooltip>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

let backupCookieRowSeq = 0;

export function emptyBackupCookieRow(): BackupCookieRow {
  backupCookieRowSeq += 1;
  return { id: `backup-cookie-${backupCookieRowSeq}`, authToken: "", csrfToken: "" };
}

export function parseBackupCookieRows(value: string): BackupCookieRow[] {
  const trimmed = value.trim();
  if (!trimmed) {
    return [emptyBackupCookieRow()];
  }
  if (trimmed === "********") {
    return [{ ...emptyBackupCookieRow(), authToken: "********", csrfToken: "********" }];
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
      return authToken || csrfToken ? [{ ...emptyBackupCookieRow(), authToken, csrfToken }] : [];
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
    .map((row) =>
      [
        row.authToken ? `auth_token=${row.authToken}` : "",
        row.csrfToken ? `ct0=${row.csrfToken}` : "",
      ]
        .filter(Boolean)
        .join("; "),
    )
    .join("\n");
}

export function isRedactedBackupCookieRow(row: BackupCookieRow) {
  return row.authToken === "********" && row.csrfToken === "********";
}
