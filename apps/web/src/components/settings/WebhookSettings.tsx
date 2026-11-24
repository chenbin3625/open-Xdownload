import React, { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellRing, Pencil, Plus, Send, Trash2 } from "lucide-react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import {
  createWebhook,
  deleteWebhook,
  getWebhooks,
  testWebhook,
  updateWebhook,
  webhooksQueryRoot,
  type Webhook,
  type WebhookPayload,
} from "../../lib/api";
import { t, useTranslation } from "../../lib/i18n";
import { formatDateTime, notifyError } from "../common/CommonUI";
import { Checkbox, Field, Input, PasswordInput, Select, Switch } from "../semi/controls";
import { Tag } from "../semi/data";
import { Modal, Popconfirm } from "../semi/overlays";
import { toast } from "../semi/toast";

const WEBHOOK_KINDS = [
  { value: "generic", labelKey: "通用 Webhook (JSON + HMAC)" },
  { value: "telegram", labelKey: "Telegram Bot" },
  { value: "discord", labelKey: "Discord Webhook" },
  { value: "bark", labelKey: "Bark 推送" },
  { value: "feishu", labelKey: "飞书机器人" },
  { value: "wecom", labelKey: "企业微信机器人" },
  { value: "dingtalk", labelKey: "钉钉机器人" },
] as const;

const WEBHOOK_EVENTS = [
  { value: "job.completed", labelKey: "任务完成" },
  { value: "job.failed", labelKey: "任务失败" },
  { value: "job.completed_with_errors", labelKey: "任务部分失败" },
  { value: "schedule.ran", labelKey: "定时计划触发" },
  { value: "account.disabled", labelKey: "X 账号失效/停用" },
  { value: "account.rate_limited", labelKey: "X 账号触发限流" },
] as const;

function urlPlaceholderForKind(kind: string): string {
  switch (kind) {
    case "telegram":
      return "https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<CHAT_ID>";
    case "discord":
      return "https://discord.com/api/webhooks/<ID>/<TOKEN>";
    case "bark":
      return "https://api.day.app/<DEVICE_KEY>/";
    case "feishu":
      return "https://open.feishu.cn/open-apis/bot/v2/hook/<TOKEN>";
    case "wecom":
      return "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=<KEY>";
    case "dingtalk":
      return "https://oapi.dingtalk.com/robot/send?access_token=<TOKEN>";
    default:
      return "https://example.com/webhook/open-xdownload";
  }
}

const emptyDraft = (): WebhookPayload => ({
  name: "",
  kind: "generic",
  url: "",
  secret: "",
  events: ["job.completed", "job.failed", "account.disabled"],
  enabled: true,
});

export function WebhookSettings() {
  useTranslation();
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: webhooksQueryRoot,
    queryFn: ({ signal }) => getWebhooks(signal),
    retry: false,
  });

  const [modalOpen, setModalOpen] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [draft, setDraft] = useState<WebhookPayload>(emptyDraft);

  const webhooks = Array.isArray(query.data) ? query.data : [];

  const saveMutation = useMutation({
    mutationFn: () =>
      editingId !== null
        ? updateWebhook(editingId, draft)
        : createWebhook(draft),
    onSuccess: () => {
      setModalOpen(false);
      setEditingId(null);
      setDraft(emptyDraft());
      void queryClient.invalidateQueries({ queryKey: webhooksQueryRoot });
      toast.success({ message: t("通知通道已保存") });
    },
    onError: notifyError(t("保存失败")),
  });

  const toggleMutation = useMutation({
    mutationFn: (wh: Webhook) =>
      updateWebhook(wh.id, {
        name: wh.name,
        kind: wh.kind,
        url: wh.url,
        secret: wh.secret,
        events: wh.events,
        enabled: !wh.enabled,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: webhooksQueryRoot });
    },
    onError: notifyError(t("更新失败")),
  });

  const deleteMutation = useMutation({
    mutationFn: deleteWebhook,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: webhooksQueryRoot });
      toast.success({ message: t("通知通道已删除") });
    },
    onError: notifyError(t("删除失败")),
  });

  const testMutation = useMutation({
    mutationFn: testWebhook,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: webhooksQueryRoot });
      toast.success({ message: t("测试通知已发送") });
    },
    onError: notifyError(t("测试推送失败")),
  });

  function openCreate() {
    setEditingId(null);
    setDraft(emptyDraft());
    setModalOpen(true);
  }

  function openEdit(wh: Webhook) {
    setEditingId(wh.id);
    setDraft({
      name: wh.name,
      kind: wh.kind,
      url: wh.url,
      secret: wh.secret ?? "",
      events: Array.isArray(wh.events) ? wh.events : [],
      enabled: wh.enabled,
    });
    setModalOpen(true);
  }

  function toggleEvent(ev: string, checked: boolean) {
    setDraft((current) => ({
      ...current,
      events: checked
        ? Array.from(new Set([...current.events, ev]))
        : current.events.filter((item) => item !== ev),
    }));
  }

  return (
    <section id="settings-webhooks" className="semi-settings-section space-y-4">
      <div className="settings-section-heading">
        <h2 className="settings-section-title">
          <BellRing size={16} className="shrink-0" />
          {t("Webhook 与推送通知")}
        </h2>
        <Button type="tertiary" icon={<Plus size={16} />} onClick={openCreate}>
          {t("添加通知通道")}
        </Button>
      </div>
      <p className="text-xs text-fg-muted">
        {t("支持任务完成/失败、定时计划执行及 X 账号失效/限流告警推送至 Telegram、Discord、Bark、飞书、企微、钉钉或自定义 Webhook。")}
      </p>

      {webhooks.length === 0 ? (
        <div className="rounded-card border border-dashed border-line p-4 text-center text-xs text-fg-subtle">
          {t("尚未配置推送通知通道")}
        </div>
      ) : (
        <div className="divide-y divide-line rounded-card border border-line">
          {webhooks.map((wh) => (
            <div key={wh.id} className="flex flex-wrap items-center justify-between gap-3 p-3">
              <div className="min-w-0 flex-1 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-semibold text-fg">{wh.name}</span>
                  <Tag size="sm">{wh.kind}</Tag>
                  {wh.lastStatus === "ok" && <Tag tone="success" size="sm">{t("最近推送成功")}</Tag>}
                  {wh.lastStatus === "error" && <Tag tone="danger" size="sm">{t("最近推送失败")}</Tag>}
                </div>
                <div className="truncate font-mono text-xs text-fg-muted" title={wh.url}>{wh.url}</div>
                <div className="flex flex-wrap items-center gap-1.5 text-xs text-fg-subtle">
                  {wh.events.map((ev) => (
                    <Tag key={ev} size="sm">{ev}</Tag>
                  ))}
                  {wh.lastSentAt && <span>· {formatDateTime(wh.lastSentAt)}</span>}
                </div>
                {wh.lastError && <div className="text-xs text-danger break-all">{wh.lastError}</div>}
              </div>
              <div className="flex items-center gap-2">
                <Switch
                  checked={wh.enabled}
                  ariaLabel={`${t("启用通道")} ${wh.name}`}
                  onChange={() => toggleMutation.mutate(wh)}
                />
                <Button
                  size="small"
                  type="tertiary"
                  icon={<Send size={14} />}
                  loading={testMutation.isPending && testMutation.variables === wh.id}
                  onClick={() => testMutation.mutate(wh.id)}
                >
                  {t("测试推送")}
                </Button>
                <Button
                  size="small"
                  theme="borderless"
                  type="tertiary"
                  icon={<Pencil size={14} />}
                  aria-label={`${t("编辑通道")} ${wh.name}`}
                  onClick={() => openEdit(wh)}
                />
                <Popconfirm
                  title={t("删除通知通道")}
                  description={t("确定删除通知通道 “{{value0}}”？", { value0: wh.name })}
                  danger
                  onConfirm={() => deleteMutation.mutate(wh.id)}
                >
                  <Button
                    size="small"
                    theme="borderless"
                    type="danger"
                    icon={<Trash2 size={14} />}
                    aria-label={`${t("删除通道")} ${wh.name}`}
                  />
                </Popconfirm>
              </div>
            </div>
          ))}
        </div>
      )}

      <Modal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        title={editingId !== null ? t("编辑通知通道") : t("添加通知通道")}
        footer={
          <div className="flex justify-end gap-2">
            <Button type="tertiary" onClick={() => setModalOpen(false)}>{t("取消")}</Button>
            <Button
              theme="solid"
              disabled={!draft.name.trim() || !draft.url.trim() || draft.events.length === 0 || saveMutation.isPending}
              loading={saveMutation.isPending}
              onClick={() => saveMutation.mutate()}
            >
              {t("保存通道")}
            </Button>
          </div>
        }
      >
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label={t("通道名称")}>
              <Input
                value={draft.name}
                onChange={(e) => setDraft((c) => ({ ...c, name: e.target.value }))}
                placeholder={t("例如：Telegram 个人告警")}
              />
            </Field>
            <Field label={t("推送类型")}>
              <Select
                ariaLabel={t("推送类型")}
                value={draft.kind}
                onChange={(kind) => setDraft((c) => ({ ...c, kind }))}
                options={WEBHOOK_KINDS.map((k) => ({ value: k.value, label: t(k.labelKey) }))}
              />
            </Field>
          </div>
          <Field label={t("Webhook URL")}>
            <Input
              value={draft.url}
              onChange={(e) => setDraft((c) => ({ ...c, url: e.target.value }))}
              placeholder={urlPlaceholderForKind(draft.kind)}
              className="font-mono text-xs"
            />
          </Field>
          <Field label={t("签名密钥 (可选，用于通用 HMAC-SHA256 / 飞书 / 钉钉加签)")}>
            <PasswordInput
              value={draft.secret ?? ""}
              onChange={(e) => setDraft((c) => ({ ...c, secret: e.target.value }))}
              placeholder={t("留空表示不启用签名")}
            />
          </Field>
          <div>
            <div className="mb-2 text-xs font-semibold text-fg-muted">{t("订阅事件")}</div>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
              {WEBHOOK_EVENTS.map((ev) => (
                <Checkbox
                  key={ev.value}
                  checked={draft.events.includes(ev.value)}
                  onChange={(checked) => toggleEvent(ev.value, checked)}
                  label={t(ev.labelKey)}
                />
              ))}
            </div>
          </div>
          <div className="flex items-center justify-between pt-1">
            <span className="text-xs font-semibold text-fg">{t("启用此通知通道")}</span>
            <Switch checked={draft.enabled} onChange={(enabled) => setDraft((c) => ({ ...c, enabled }))} />
          </div>
        </div>
      </Modal>
    </section>
  );
}
