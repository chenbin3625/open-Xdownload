import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  ClipboardPaste,
  HelpCircle,
  Key,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import React, { useEffect, useState } from "react";
import type { AppConfig, AuthCheck, ClientStatus } from "../../lib/api";
import { cn } from "../../lib/cn";
import { Button } from "../ui/Button";
import { Alert } from "../ui/Feedback";
import { Field, PasswordInput, Textarea } from "../ui/Input";
import { Tooltip } from "../ui/Overlay";
import { Tag, type Tone } from "../ui/Tag";
import { EnvOverrideNote } from "./EnvOverrideNote";

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
      <div>
        可点「批量粘贴」一次导入多个账号：每行一个，支持{" "}
        <code className="font-mono">auth_token=xxx; ct0=yyy</code> 或 JSON 数组。
      </div>
      <div>已保存的 Cookie 在此界面会以 ******** 脱敏显示，新增或修改不受影响。</div>
    </div>
  ),
};

// 与 DownloadSettings 的 TipIcon 保持一致：用可聚焦的 <button>，
// 键盘用户也能读到说明；<span> 只能悬停触发，读屏/键盘用户拿不到。
function TipIcon({ content }: { content: React.ReactNode }) {
  return (
    <Tooltip content={content}>
      <button
        type="button"
        aria-label="查看说明"
        className="inline-flex cursor-help text-fg-subtle hover:text-fg-muted"
      >
        <HelpCircle className="size-3.5" />
      </button>
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
  envOverridden = [],
}: {
  authError: string;
  authResult: AuthCheck | null;
  checking: boolean;
  draft: AppConfig;
  onChange: React.Dispatch<React.SetStateAction<AppConfig>>;
  envOverridden?: string[];
}) {
  const clients = authResult?.diagnostics?.clients ?? [];
  const primaryClient = clients.find((client) => client.primary) ?? clients[0];
  const backupClients = clients.filter((client) => !client.primary);
  const isClearSentinel = (v: string) => v === "__CLEAR__";
  const primaryConfigured = Boolean(
    (draft.authToken ?? "").trim() || (draft.csrfToken ?? "").trim(),
  );
  const primaryComplete = Boolean(
    (draft.authToken ?? "").trim() && !isClearSentinel(draft.authToken ?? "") &&
    (draft.csrfToken ?? "").trim() && !isClearSentinel(draft.csrfToken ?? ""),
  );
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
          {primaryConfigured && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() =>
                onChange((current) => ({
                  ...current,
                  authToken: "__CLEAR__",
                  csrfToken: "__CLEAR__",
                }))
              }
            >
              <Trash2 className="size-3.5" />
              清除
            </Button>
          )}
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
                  hasValue={Boolean((draft.authToken ?? "").trim()) && !isClearSentinel(draft.authToken ?? "")}
                  pairComplete={primaryComplete}
                />
              }
              value={isClearSentinel(draft.authToken ?? "") ? "" : (draft.authToken ?? "")}
              onChange={(event) =>
                onChange((current) => ({ ...current, authToken: event.target.value }))
              }
              placeholder="输入 auth_token"
            />
            <EnvOverrideNote show={envOverridden.includes("authToken")} />
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
                  hasValue={Boolean((draft.csrfToken ?? "").trim()) && !isClearSentinel(draft.csrfToken ?? "")}
                  pairComplete={primaryComplete}
                />
              }
              value={isClearSentinel(draft.csrfToken ?? "") ? "" : (draft.csrfToken ?? "")}
              onChange={(event) =>
                onChange((current) => ({ ...current, csrfToken: event.target.value }))
              }
              placeholder="输入 ct0"
            />
            <EnvOverrideNote show={envOverridden.includes("csrfToken")} />
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
        <EnvOverrideNote show={envOverridden.includes("additionalCookies")} />
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
  const [batchOpen, setBatchOpen] = useState(false);
  const [batchText, setBatchText] = useState("");
  const batchHelpId = React.useId();
  // 只在面板打开时解析，并按文本记忆：每次按键都 emptyBackupCookieRow() 生成 id 太浪费。
  const parsedBatch = React.useMemo(
    () => (batchOpen ? parsePastedCookieRows(batchText) : null),
    [batchOpen, batchText],
  );

  useEffect(() => {
    const nextRows = parseBackupCookieRows(value);
    const nextValue = normalizeBackupCookieRows(nextRows);
    setRows((currentRows) =>
      normalizeBackupCookieRows(currentRows) === nextValue ? currentRows : nextRows,
    );
  }, [value]);

  function updateRow(index: number, field: keyof BackupCookieRow, nextValue: string) {
    const nextRows = rows.map((row, rowIndex) => {
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
    setRows(nextRows);
    onChange(normalizeBackupCookieRows(nextRows));
  }

  function addRow() {
    const nextRows = [...rows, emptyBackupCookieRow()];
    setRows(nextRows);
    onChange(normalizeBackupCookieRows(nextRows));
  }

  function removeRow(index: number) {
    const nextRows = rows.filter((_, rowIndex) => rowIndex !== index);
    const safeRows = nextRows.length > 0 ? nextRows : [emptyBackupCookieRow()];
    setRows(safeRows);
    onChange(normalizeBackupCookieRows(safeRows));
  }

  // 批量粘贴：把解析出的账号追加到现有池子（空占位行会被丢掉）。
  function importPastedRows() {
    const parsed = parsePastedCookieRows(batchText);
    if (parsed.rows.length === 0) return;
    const base = rows.filter((row) => row.authToken.trim() || row.csrfToken.trim());
    const nextRows = [...base, ...parsed.rows];
    setRows(nextRows);
    onChange(normalizeBackupCookieRows(nextRows));
    setBatchText("");
    setBatchOpen(false);
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-1.5">
          <span className="text-sm font-semibold text-fg">备用 Cookie 多账号池</span>
          <TipIcon content={cookieSettingsTips.backupCookie} />
        </div>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="secondary"
            icon={<ClipboardPaste className="size-3.5" />}
            onClick={() => setBatchOpen((open) => !open)}
            aria-expanded={batchOpen}
            aria-controls={`${batchHelpId}-panel`}
          >
            批量粘贴
          </Button>
          <Button
            size="sm"
            variant="secondary"
            icon={<Plus className="size-3.5" />}
            onClick={addRow}
          >
            添加备用账号
          </Button>
        </div>
      </div>

      {batchOpen && parsedBatch && (() => {
        const canImport = parsedBatch.rows.length > 0;
        return (
          <div
            id={`${batchHelpId}-panel`}
            className="space-y-2 rounded-control border border-line bg-surface-muted/40 p-2.5"
          >
            <p id={batchHelpId} className="text-xs text-fg-muted">
              每行一个账号，支持{" "}
              <code className="font-mono">auth_token=xxx; ct0=yyy</code> 或 JSON 数组；
              粘贴后点「导入」，已保存的账号不受影响。
            </p>
            <Textarea
              rows={4}
              aria-label="批量粘贴 Cookie 内容"
              aria-describedby={batchHelpId}
              value={batchText}
              onChange={(event) => setBatchText(event.target.value)}
              placeholder={"auth_token=xxx; ct0=yyy\nauth_token=aaa; ct0=bbb"}
              className="font-mono text-xs"
            />
            {batchText.trim() && parsedBatch.error && (
              <p className={cn("text-xs", canImport ? "text-warning-strong" : "text-danger-strong")}>
                {parsedBatch.error}
              </p>
            )}
            <div className="flex items-center justify-end gap-2">
              <Button
                size="sm"
                variant="text"
                onClick={() => {
                  setBatchOpen(false);
                  setBatchText("");
                }}
              >
                取消
              </Button>
              <Button size="sm" variant="primary" disabled={!canImport} onClick={importPastedRows}>
                导入{canImport ? ` ${parsedBatch.rows.length} 个账号` : ""}
              </Button>
            </div>
          </div>
        );
      })()}

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

          // 半行（只填了 auth_token 或只填了 ct0）：后端现在允许保存，
          // 但这种账号不参与轮询，必须在本行给出明确提示，别让用户以为已经生效。
          const hasAnyValue = Boolean(row.authToken.trim() || row.csrfToken.trim());
          const missingField = row.authToken.trim() ? "ct0" : "auth_token";
          const showIncompleteHint = !pairComplete && hasAnyValue;

          return (
            <div
              key={row.id}
              className="flex flex-col gap-1.5 rounded-control border border-line bg-surface-muted/40 p-2.5"
            >
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
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
              {showIncompleteHint && (
                <p className="flex items-center gap-1 text-[11px] text-warning-strong">
                  <AlertTriangle className="size-3 shrink-0" aria-hidden="true" />
                  缺少 {missingField}：只填一半的账号不会参与轮询，请补全或删除这一行。
                </p>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

export function emptyBackupCookieRow(): BackupCookieRow {
  const id = Array.from(crypto.getRandomValues(new Uint32Array(4)), (part) =>
    part.toString(16).padStart(8, "0"),
  ).join("");
  return { id: `backup-cookie-${id}`, authToken: "", csrfToken: "" };
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
      return authToken || csrfToken
        ? [{ id: firstString(record.id) || emptyBackupCookieRow().id, authToken, csrfToken }]
        : [];
    });
  } catch {
    return [];
  }
}

export type PastedCookieParseResult = {
  /** 可追加到账号池的行；为空表示没有解析出任何账号 */
  rows: BackupCookieRow[];
  /** 非空时展示给用户（rows 非空表示"能导入但有警告"，rows 为空表示"不能导入"） */
  error: string;
};

// 批量粘贴解析。支持两种输入：
//   1) JSON 数组：[{"authToken":"..","csrfToken":".."}]（也接受 auth_token / ct0 键名）
//   2) 每行一个账号的文本：auth_token=xxx; ct0=yyy（也接受 "auth_token: xxx" 写法、markdown 列表前缀）
// 只解析不落库；导入逻辑见 BackupCookieInputs.importPastedRows。
export function parsePastedCookieRows(text: string): PastedCookieParseResult {
  const trimmed = text.trim();
  if (!trimmed) {
    return { rows: [], error: "粘贴内容为空" };
  }

  if (trimmed.startsWith("[") || trimmed.startsWith("{")) {
    const jsonRows = parseBackupCookieRowsFromJSON(trimmed);
    if (jsonRows.length > 0) {
      return { rows: jsonRows, error: "" };
    }
    return { rows: [], error: "JSON 无法解析出 auth_token / ct0，请检查格式" };
  }

  const rows: BackupCookieRow[] = [];
  for (const rawLine of trimmed.split(/\r?\n/)) {
    const line = rawLine.trim().replace(/^[-*]\s+/, "");
    if (!line) continue;
    const row = emptyBackupCookieRow();
    // 冒号写法（auth_token: xxx）整行是一个键值对；其余按 ; , 空白分词
    if (line.includes(":") && !/[;,]/.test(line)) {
      setBackupCookieValue(row, line);
    } else {
      for (const token of line.split(/[;,\s]+/)) {
        setBackupCookieValue(row, token);
      }
    }
    if (row.authToken.trim() || row.csrfToken.trim()) {
      rows.push(row);
    }
  }

  if (rows.length === 0) {
    return {
      rows: [],
      error: "没有识别到账号：每行应为 auth_token=xxx; ct0=yyy，或粘贴 JSON 数组",
    };
  }
  const incomplete = rows.filter((row) => !row.authToken.trim() || !row.csrfToken.trim()).length;
  return {
    rows,
    error:
      incomplete > 0
        ? `有 ${incomplete} 行只识别到一个字段，导入后需要补全（缺字段的账号不参与轮询）`
        : "",
  };
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
  const populatedRows = rows
    .map((row) => ({ id: row.id, authToken: row.authToken.trim(), csrfToken: row.csrfToken.trim() }))
    .filter((row) => Boolean(row.authToken || row.csrfToken));
  return populatedRows.length > 0 ? JSON.stringify(populatedRows) : "";
}

export function isRedactedBackupCookieRow(row: BackupCookieRow) {
  return row.authToken === "********" && row.csrfToken === "********";
}
