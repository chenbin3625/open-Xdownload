import { t, useTranslation } from "../../lib/i18n";
import {
  AlertCircle,
  CheckCircle2,
  LockKeyhole,
  Pencil,
  RefreshCw,
  Star,
  Trash2,
  Unlock,
  Upload,
  Users,
} from "lucide-react";
import React, { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@douyinfe/semi-ui";
import {
  checkAllXAccounts,
  checkXAccount,
  deleteXAccount,
  getXAccounts,
  importXAccounts,
  unlockXAccount,
  updateXAccount,
  xAccountsQueryRoot,
  type XAccountImportResult,
  type XAccountLock,
  type XAccountPatch,
  type XAccountView,
} from "../../lib/api";
import { formatDateTime, getErrorMessage, notifyError } from "../common/CommonUI";
import {
  Alert,
  Empty,
  Field,
  Input,
  Modal,
  Popconfirm,
  Switch,
  Table,
  Tag,
  Textarea,
  Tooltip,
  toast,
  type TableColumn,
} from "../semi";
import { cn } from "../../lib/cn";

const importFormats = [
  {
    example: "auth_token=xxx; ct0=yyy",
    label: t("Cookie 请求头字符串：一行一个账号"),
  },
  {
    example: "auth_token ct0 / auth_token,ct0",
    label: t("两个裸值：auth_token 与 ct0，空格或逗号分隔"),
  },
  {
    example: '{"auth_token":"xxx","ct0":"yyy"} 或 [{...}]',
    label: t("JSON 对象或数组"),
  },
  {
    example: '[{"name":"auth_token","value":"xxx"},{"name":"ct0","value":"yyy"}]',
    label: t("浏览器扩展导出的 [{name,value}] 数组"),
  },
];

function accountLabel(account: XAccountView) {
  return account.screenName ? `@${account.screenName}` : account.authToken;
}

function queueLabel(queue: string) {
  if (queue === "*") {
    return t("账号级冷却");
  }
  return queue;
}

/** 剩余时间只用于展示，不参与调度；过期锁每次渲染时过滤，无需等刷新。 */
function formatRemaining(until: string, now: number) {
  const target = new Date(until).getTime();
  if (!Number.isFinite(target) || target <= now) {
    return "";
  }
  const totalSeconds = Math.ceil((target - now) / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes >= 60) {
    const hours = Math.floor(minutes / 60);
    return t("{{value0}} 小时 {{value1}} 分", { value0: hours, value1: minutes % 60 });
  }
  if (minutes > 0) {
    return t("{{value0}} 分 {{value1}} 秒", { value0: minutes, value1: seconds });
  }
  return t("{{value0}} 秒", { value0: seconds });
}

function activeLocks(locks: XAccountLock[], now: number) {
  return locks.filter((lock) => {
    const until = new Date(lock.until).getTime();
    return Number.isFinite(until) && until > now;
  });
}

/** 只有存在未过期锁时才需要每秒刷新剩余时间。 */
function useNow(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) {
      return;
    }
    const timer = window.setInterval(() => setNow(Date.now()), 1_000);
    return () => window.clearInterval(timer);
  }, [active]);
  return now;
}

export function XAccountsSettings() {
  useTranslation();
  const queryClient = useQueryClient();
  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState("");
  const [importResult, setImportResult] = useState<XAccountImportResult | null>(null);
  const [editing, setEditing] = useState<XAccountView | null>(null);
  const [editUserAgent, setEditUserAgent] = useState("");
  const [editProxy, setEditProxy] = useState("");
  const [editCookie, setEditCookie] = useState("");

  const query = useQuery({
    queryKey: xAccountsQueryRoot,
    queryFn: ({ signal }) => getXAccounts(signal),
  });
  const accounts = query.data?.accounts ?? [];
  const scraping = query.data?.scraper;
  const now = useNow(accounts.some((account) => account.locks.length > 0));

  const refresh = () => queryClient.invalidateQueries({ queryKey: xAccountsQueryRoot });

  const importMutation = useMutation({
    mutationFn: importXAccounts,
    onSuccess: (result) => {
      setImportResult(result);
      void refresh();
    },
    onError: notifyError(t("导入账号失败")),
  });

  const checkOne = useMutation({
    mutationFn: checkXAccount,
    onSuccess: (result) => {
      if (result.ok) {
        toast.success({
          message: t("账号检测通过"),
          description: result.screenName ? `@${result.screenName}` : undefined,
        });
      } else {
        toast.warning({
          message: t("账号检测未通过"),
          description: result.error ?? result.account.errorMsg,
        });
      }
      void refresh();
    },
    onError: notifyError(t("账号检测失败")),
  });

  const checkAll = useMutation({
    mutationFn: checkAllXAccounts,
    onSuccess: (result) => {
      const failed = result.results.filter((item) => !item.ok).length;
      const description = t("成功 {{value0}} 个，失败 {{value1}} 个", {
        value0: result.results.length - failed,
        value1: failed,
      });
      if (failed > 0) {
        toast.warning({ message: t("全部检测完成，部分账号异常"), description });
      } else {
        toast.success({ message: t("全部检测完成"), description });
      }
      void refresh();
    },
    onError: notifyError(t("全部检测失败")),
  });

  const update = useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: XAccountPatch }) =>
      updateXAccount(id, patch),
    onSuccess: () => void refresh(),
    onError: notifyError(t("更新账号失败")),
  });

  const remove = useMutation({
    mutationFn: deleteXAccount,
    onSuccess: () => {
      toast.success({ message: t("账号已删除") });
      void refresh();
    },
    onError: notifyError(t("删除账号失败")),
  });

  const unlock = useMutation({
    mutationFn: unlockXAccount,
    onSuccess: () => {
      toast.success({ message: t("账号已解锁") });
      void refresh();
    },
    onError: notifyError(t("解锁账号失败")),
  });

  function openImport() {
    setImportText("");
    setImportResult(null);
    setImportOpen(true);
  }

  function submitImport() {
    const text = importText.trim();
    if (!text || importMutation.isPending) {
      return;
    }
    importMutation.mutate(text);
  }

  function openEdit(account: XAccountView) {
    setEditing(account);
    setEditUserAgent(account.userAgent);
    setEditProxy(account.proxy);
    setEditCookie("");
  }

  function submitEdit() {
    if (!editing || update.isPending) {
      return;
    }
    const patch: XAccountPatch = {};
    if (editUserAgent.trim() !== editing.userAgent) {
      patch.userAgent = editUserAgent.trim();
    }
    if (editProxy.trim() !== editing.proxy) {
      patch.proxy = editProxy.trim();
    }
    if (editing.source !== "env" && editCookie.trim()) {
      patch.cookie = editCookie.trim();
    }
    if (Object.keys(patch).length === 0) {
      setEditing(null);
      return;
    }
    update.mutate(
      { id: editing.id, patch },
      {
        onSuccess: () => {
          setEditing(null);
          toast.success({ message: t("账号已更新") });
        },
      },
    );
  }

  const updatePendingId = update.isPending ? update.variables?.id : undefined;
  const deletePendingId = remove.isPending ? remove.variables : undefined;
  const unlockPendingId = unlock.isPending ? unlock.variables : undefined;
  const checkPendingId = checkOne.isPending ? checkOne.variables : undefined;

  const columns: TableColumn<XAccountView>[] = [
    {
      key: "account",
      title: t("账号"),
      width: 170,
      render: (account) => (
        <div className="x-account-identity">
          {account.screenName ? (
            <span className="x-account-name">@{account.screenName}</span>
          ) : (
            <code className="x-account-token">{account.authToken}</code>
          )}
          <span className="x-account-id">#{account.id}</span>
        </div>
      ),
    },
    {
      key: "primary",
      title: t("主账号"),
      width: 76,
      align: "center",
      render: (account) =>
        account.isPrimary ? (
          <Tag tone="brand" size="sm" icon={<Star className="size-3" />}>
            {t("主账号")}
          </Tag>
        ) : (
          <span className="text-fg-subtle">—</span>
        ),
    },
    {
      key: "source",
      title: t("来源"),
      width: 110,
      render: (account) =>
        account.source === "env" ? (
          <Tooltip content={t("环境变量账号为只读，不能删除或替换 Cookie")}>
            <span tabIndex={0}>
              <Tag tone="neutral" size="sm" icon={<LockKeyhole className="size-3" />}>
                {t("环境变量 · 只读")}
              </Tag>
            </span>
          </Tooltip>
        ) : (
          <Tag tone="info" size="sm">
            {t("本地导入")}
          </Tag>
        ),
    },
    {
      key: "status",
      title: t("状态"),
      width: 120,
      render: (account) => (
        <div className="x-account-status">
          <Tag tone={account.active ? "success" : "danger"} size="sm">
            {account.active ? t("启用") : t("停用")}
          </Tag>
          {account.errorMsg && <p className="x-account-error">{account.errorMsg}</p>}
        </div>
      ),
    },
    {
      key: "locks",
      title: t("当前锁"),
      width: 170,
      render: (account) => {
        const locks = activeLocks(account.locks, now);
        if (locks.length === 0) {
          return <span className="text-fg-subtle">—</span>;
        }
        return (
          <div className="x-account-locks">
            {locks.map((lock) => (
              <span
                key={lock.queue}
                className="x-account-lock"
                title={
                  lock.reason
                    ? `${formatDateTime(lock.until)} · ${lock.reason}`
                    : formatDateTime(lock.until)
                }
              >
                <Tag tone="warning" size="sm">
                  {queueLabel(lock.queue)}
                </Tag>
                <span className="text-xs text-fg-muted">
                  {t("剩余 {{value0}}", { value0: formatRemaining(lock.until, now) })}
                </span>
              </span>
            ))}
          </div>
        );
      },
    },
    {
      key: "requests",
      title: t("今日请求数"),
      width: 96,
      align: "right",
      render: (account) => <span className="font-mono">{account.requestsToday}</span>,
    },
    {
      key: "lastUsed",
      title: t("最后使用时间"),
      width: 130,
      render: (account) =>
        account.lastUsed ? (
          <span className="text-xs">{formatDateTime(account.lastUsed)}</span>
        ) : (
          <span className="text-xs text-fg-subtle">{t("从未使用")}</span>
        ),
    },
    {
      key: "actions",
      title: t("操作"),
      width: 200,
      render: (account) => {
        const label = accountLabel(account);
        const isEnv = account.source === "env";
        const busy = updatePendingId === account.id;
        const locks = activeLocks(account.locks, now);
        return (
          <div className="x-account-actions">
            <Tooltip content={t("检测")}>
              <Button
                theme="borderless"
                type="tertiary"
                icon={<RefreshCw className={cn("size-3.5", checkPendingId === account.id && "animate-spin")} />}
                aria-label={`${t("检测")} ${label}`}
                loading={checkPendingId === account.id}
                disabled={checkOne.isPending}
                onClick={() => checkOne.mutate(account.id)}
              />
            </Tooltip>
            <Tooltip content={account.active ? t("停用") : t("启用")}>
              <span>
                <Switch
                  checked={account.active}
                  loading={busy && update.variables?.patch.active !== undefined}
                  disabled={busy}
                  ariaLabel={`${t("启用状态")} ${label}`}
                  onChange={(checked) =>
                    update.mutate({ id: account.id, patch: { active: checked } })
                  }
                />
              </span>
            </Tooltip>
            <Tooltip content={account.isPrimary ? t("已是主账号") : t("设为主账号")}>
              <Button
                theme="borderless"
                type="tertiary"
                icon={<Star className={cn("size-3.5", account.isPrimary && "fill-current")} />}
                aria-label={`${t("设为主账号")} ${label}`}
                disabled={account.isPrimary || busy}
                onClick={() => update.mutate({ id: account.id, patch: { isPrimary: true } })}
              />
            </Tooltip>
            <Tooltip content={t("编辑")}>
              <Button
                theme="borderless"
                type="tertiary"
                icon={<Pencil className="size-3.5" />}
                aria-label={`${t("编辑")} ${label}`}
                disabled={busy}
                onClick={() => openEdit(account)}
              />
            </Tooltip>
            {locks.length > 0 && (
              <Tooltip content={t("解锁")}>
                <Button
                  theme="borderless"
                  type="tertiary"
                  icon={<Unlock className="size-3.5" />}
                  aria-label={`${t("解锁")} ${label}`}
                  loading={unlockPendingId === account.id}
                  disabled={unlock.isPending}
                  onClick={() => unlock.mutate(account.id)}
                />
              </Tooltip>
            )}
            {isEnv ? (
              <Tooltip content={t("环境变量账号为只读，不能删除")}>
                <span tabIndex={0}>
                  <Button
                    theme="borderless"
                    type="danger"
                    icon={<Trash2 className="size-3.5" />}
                    aria-label={`${t("删除")} ${label}`}
                    title={t("环境变量账号为只读，不能删除")}
                    disabled
                  />
                </span>
              </Tooltip>
            ) : (
              <Popconfirm
                title={t("删除账号？")}
                description={t("将同时删除该账号的 Cookie、锁和请求统计。")}
                okText={t("删除")}
                cancelText={t("取消")}
                danger
                onConfirm={() => remove.mutate(account.id)}
              >
                <Button
                  theme="borderless"
                  type="danger"
                  icon={<Trash2 className="size-3.5" />}
                  aria-label={`${t("删除")} ${label}`}
                  loading={deletePendingId === account.id}
                  disabled={remove.isPending}
                />
              </Popconfirm>
            )}
          </div>
        );
      },
    },
  ];

  const heading = (
    <div className="settings-section-heading x-accounts-heading">
      <div className="x-accounts-title">
        <h2 className="settings-section-title">
          <Users size={16} />
          {t("X 账号池")}
        </h2>
        <Tooltip content={t("当前后端生效的爬虫实现，由 OPEN_XDOWNLOAD_SCRAPER 决定")}>
          <span tabIndex={0}>
            <Tag tone={scraping === "xscrape" ? "brand" : "neutral"} size="sm">
              {scraping ?? t("未知")}
            </Tag>
          </span>
        </Tooltip>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="tertiary"
          icon={<RefreshCw className={cn("size-4", checkAll.isPending && "animate-spin")} />}
          loading={checkAll.isPending}
          disabled={checkAll.isPending || accounts.length === 0}
          onClick={() => checkAll.mutate()}
        >
          {t("全部检测")}
        </Button>
        <Button
          theme="solid"
          icon={<Upload className="size-4" />}
          onClick={openImport}
        >
          {t("导入账号")}
        </Button>
      </div>
    </div>
  );

  return (
    <section className="semi-settings-section settings-accounts">
      {heading}
      <p className="settings-section-description">
        {t("账号只通过导入 Cookie 添加，用于在多个账号间轮换以降低限流概率。")}
      </p>

      {query.isError ? (
        <Alert
          type="error"
          message={t("加载账号列表失败")}
          description={getErrorMessage(query.error)}
          action={<Button onClick={() => void query.refetch()}>{t("重试")}</Button>}
        />
      ) : (
        <Table<XAccountView>
          columns={columns}
          data={accounts}
          rowKey={(account) => account.id}
          loading={query.isLoading}
          minWidth={1040}
          className="x-accounts-table"
          emptyText={
            <Empty
              description={
                <div className="x-accounts-empty">
                  <p>{t("还没有 X 账号。导入第一个账号后即可开始下载。")}</p>
                  <Button icon={<Upload className="size-4" />} onClick={openImport}>
                    {t("导入第一个账号")}
                  </Button>
                </div>
              }
            />
          }
          expandedRowRender={(account) => (
            <div className="x-account-requests">
              <h3 className="settings-subsection-title">{t("今日请求明细")}</h3>
              {account.requestsByQueue.length > 0 ? (
                <ul className="x-account-request-list">
                  {account.requestsByQueue.map((item) => (
                    <li key={item.queue}>
                      <span className="font-mono">{queueLabel(item.queue)}</span>
                      <span>{item.requests}</span>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-xs text-fg-muted">{t("今天还没有请求")}</p>
              )}
            </div>
          )}
        />
      )}

      <Modal
        open={importOpen}
        onClose={() => {
          if (!importMutation.isPending) {
            setImportOpen(false);
          }
        }}
        title={t("导入 X 账号")}
        footer={
          <>
            <Button
              type="tertiary"
              disabled={importMutation.isPending}
              onClick={() => setImportOpen(false)}
            >
              {t("关闭")}
            </Button>
            <Button
              theme="solid"
              icon={<Upload className="size-4" />}
              loading={importMutation.isPending}
              disabled={!importText.trim() || importMutation.isPending}
              onClick={submitImport}
            >
              {t("导入")}
            </Button>
          </>
        }
      >
        <div className="x-account-import">
          <p className="text-sm text-fg-body">
            {t("需要登录 x.com 后从浏览器复制 Cookie。支持以下 4 种格式：")}
          </p>
          <ul className="x-account-import-formats">
            {importFormats.map((format) => (
              <li key={format.label}>
                <span>{format.label}</span>
                <code>{format.example}</code>
              </li>
            ))}
          </ul>
          <Field label={t("账号导入内容")}>
            <Textarea
              rows={7}
              value={importText}
              aria-label={t("账号导入内容")}
              placeholder={"auth_token=xxx; ct0=yyy\n{\"auth_token\":\"xxx\",\"ct0\":\"yyy\"}"}
              className="font-mono text-xs"
              disabled={importMutation.isPending}
              onChange={(event) => setImportText(event.target.value)}
            />
          </Field>
          {importResult && (
            <div className="x-account-import-result" role="status">
              <Alert
                type={importResult.created.length > 0 ? "success" : "warning"}
                message={t("已导入 {{value0}} 个账号", { value0: importResult.created.length })}
                description={
                  importResult.created.length > 0
                    ? importResult.created.map(accountLabel).join("、")
                    : undefined
                }
              />
              {importResult.issues.length > 0 && (
                <div className="x-account-import-issues">
                  <p className="text-xs font-medium text-fg-body">
                    {t("以下 {{value0}} 个条目未导入：", { value0: importResult.issues.length })}
                  </p>
                  <ul>
                    {importResult.issues.map((issue) => (
                      <li key={`${issue.index}-${issue.reason}`}>
                        <span className="font-medium">
                          {t("第 {{value0}} 条", { value0: issue.index + 1 })}
                        </span>
                        <span>{issue.reason}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          )}
        </div>
      </Modal>

      <Modal
        open={editing !== null}
        onClose={() => {
          if (!update.isPending) {
            setEditing(null);
          }
        }}
        title={t("编辑账号")}
        description={editing ? accountLabel(editing) : undefined}
        footer={
          <>
            <Button
              type="tertiary"
              disabled={update.isPending}
              onClick={() => setEditing(null)}
            >
              {t("取消")}
            </Button>
            <Button
              theme="solid"
              loading={update.isPending}
              disabled={update.isPending}
              onClick={submitEdit}
            >
              {t("保存")}
            </Button>
          </>
        }
      >
        <div className="grid gap-4">
          <Field label={t("User-Agent（留空使用全局默认）")}>
            <Input
              value={editUserAgent}
              aria-label={t("User-Agent")}
              disabled={update.isPending}
              onChange={(event) => setEditUserAgent(event.target.value)}
            />
          </Field>
          <Field label={t("代理（留空使用全局 Proxy）")}>
            <Input
              value={editProxy}
              aria-label={t("代理")}
              placeholder={t("http://127.0.0.1:7890 或 socks5://127.0.0.1:1080")}
              disabled={update.isPending}
              onChange={(event) => setEditProxy(event.target.value)}
            />
          </Field>
          {editing?.source === "env" ? (
            <Alert
              type="info"
              message={t("环境变量账号的 Cookie 由环境变量提供，不能在这里替换。")}
            />
          ) : (
            <Field label={t("替换 Cookie（留空则不修改）")}>
              <Textarea
                rows={4}
                value={editCookie}
                aria-label={t("替换 Cookie")}
                placeholder={"auth_token=xxx; ct0=yyy"}
                className="font-mono text-xs"
                disabled={update.isPending}
                onChange={(event) => setEditCookie(event.target.value)}
              />
            </Field>
          )}
        </div>
      </Modal>
    </section>
  );
}
