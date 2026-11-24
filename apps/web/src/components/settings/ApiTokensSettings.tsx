import React, { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Download, KeyRound, Plus, Puzzle, Trash2 } from "lucide-react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import {
  apiTokensQueryRoot,
  createApiToken,
  deleteApiToken,
  getApiTokens,
} from "../../lib/api";
import { t, useTranslation } from "../../lib/i18n";
import { copyToClipboard, formatDateTime, notifyError } from "../common/CommonUI";
import { Field, Input, NumberInput } from "../semi/controls";
import { Alert } from "../semi/feedback";
import { Modal, Popconfirm } from "../semi/overlays";
import { toast } from "../semi/toast";

export function ApiTokensSettings() {
  useTranslation();
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: apiTokensQueryRoot,
    queryFn: ({ signal }) => getApiTokens(signal),
    retry: false,
  });

  const [createOpen, setCreateOpen] = useState(false);
  const [tokenName, setTokenName] = useState("");
  const [expiresInDays, setExpiresInDays] = useState<number | null>(null);
  const [revealedToken, setRevealedToken] = useState<string | null>(null);

  const tokens = Array.isArray(query.data) ? query.data : [];

  const createMutation = useMutation({
    mutationFn: () =>
      createApiToken({
        name: tokenName.trim(),
        expiresInDays: expiresInDays && expiresInDays > 0 ? expiresInDays : undefined,
      }),
    onSuccess: (res) => {
      setCreateOpen(false);
      setTokenName("");
      setExpiresInDays(null);
      setRevealedToken(res.plaintext);
      void queryClient.invalidateQueries({ queryKey: apiTokensQueryRoot });
      toast.success({ message: t("API 令牌已创建") });
    },
    onError: notifyError(t("创建失败")),
  });

  const deleteMutation = useMutation({
    mutationFn: deleteApiToken,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: apiTokensQueryRoot });
      toast.success({ message: t("API 令牌已吊销") });
    },
    onError: notifyError(t("删除失败")),
  });

  return (
    <section id="settings-integrations" className="semi-settings-section space-y-4">
      <div className="settings-section-heading">
        <h2 className="settings-section-title">
          <KeyRound size={16} className="shrink-0" />
          {t("API 令牌与浏览器扩展")}
        </h2>
        <Button type="tertiary" icon={<Plus size={16} />} onClick={() => setCreateOpen(true)}>
          {t("创建 API 令牌")}
        </Button>
      </div>
      <p className="text-xs text-fg-muted">
        {t("Personal API Token (oxd_...) 可用于浏览器油猴脚本、iOS 快捷指令、自动化脚本以及 RSS 订阅鉴权。")}
      </p>

      {revealedToken && (
        <Alert
          type="warning"
          showIcon
          message={t("请立即复制并妥善保存新令牌，关闭后将无法再次查看明文")}
          description={
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <code className="rounded bg-surface-muted px-2 py-1 font-mono text-xs break-all text-fg">{revealedToken}</code>
              <Button size="small" icon={<Copy size={14} />} onClick={() => void copyToClipboard(revealedToken, t("API 令牌"))}>
                {t("复制令牌")}
              </Button>
              <Button size="small" theme="borderless" type="tertiary" onClick={() => setRevealedToken(null)}>
                {t("我已保存")}
              </Button>
            </div>
          }
        />
      )}

      {tokens.length === 0 ? (
        <div className="rounded-card border border-dashed border-line p-4 text-center text-xs text-fg-subtle">
          {t("尚未创建 Personal API Token")}
        </div>
      ) : (
        <div className="divide-y divide-line rounded-card border border-line">
          {tokens.map((tok) => (
            <div key={tok.id} className="flex flex-wrap items-center justify-between gap-2 p-3 text-xs">
              <div className="min-w-0 space-y-0.5">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-semibold text-fg">{tok.name}</span>
                  <code className="rounded bg-surface-muted px-1.5 py-0.5 font-mono text-fg-muted">{tok.tokenPrefix}…</code>
                </div>
                <div className="text-fg-subtle">
                  {t("创建于")}: {formatDateTime(tok.createdAt)}
                  {tok.lastUsedAt ? ` · ${t("最近使用")}: ${formatDateTime(tok.lastUsedAt)}` : ` · ${t("尚未使用")}`}
                  {tok.expiresAt ? ` · ${t("过期时间")}: ${formatDateTime(tok.expiresAt)}` : ` · ${t("永不过期")}`}
                </div>
              </div>
              <Popconfirm
                title={t("吊销 API 令牌")}
                description={t("确定吊销令牌 “{{value0}}”？使用该令牌的脚本与 RSS 订阅将立即失效。", { value0: tok.name })}
                danger
                onConfirm={() => deleteMutation.mutate(tok.id)}
              >
                <Button
                  size="small"
                  theme="borderless"
                  type="danger"
                  icon={<Trash2 size={14} />}
                  aria-label={`${t("吊销令牌")} ${tok.name}`}
                />
              </Popconfirm>
            </div>
          ))}
        </div>
      )}

      {/* 浏览器伴侣脚本与外部集成卡片 */}
      <div className="rounded-card border border-line bg-surface-muted/40 p-3.5 space-y-2.5 text-xs">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2 font-semibold text-fg text-sm">
            <Puzzle size={16} className="text-brand-500" />
            <span>{t("浏览器伴侣脚本 (Tampermonkey / Violentmonkey)")}</span>
          </div>
          <a
            href="/api/extensions/userscript.user.js"
            download="open-xdownload.user.js"
            className="semi-button semi-button-light semi-button-primary semi-button-size-small"
          >
            <Download size={14} />
            <span>{t("下载安装伴侣脚本 (.user.js)")}</span>
          </a>
        </div>
        <p className="text-fg-muted">
          {t("安装后将在 X 页面每条推文旁注入“归档”按钮（按住 Shift 归档完整长串），并支持通过油猴菜单一键同步当前登录 X Cookie 到账号池。")}
        </p>
        <div className="text-fg-subtle font-mono break-all">
          {t("RSS 订阅示例")}: {typeof window !== "undefined" ? window.location.origin : ""}/api/rss/tweets?token=oxd_...
        </div>
      </div>

      <Modal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        title={t("创建 API 令牌")}
        footer={
          <div className="flex justify-end gap-2">
            <Button type="tertiary" onClick={() => setCreateOpen(false)}>{t("取消")}</Button>
            <Button
              theme="solid"
              disabled={!tokenName.trim() || createMutation.isPending}
              loading={createMutation.isPending}
              onClick={() => createMutation.mutate()}
            >
              {t("生成令牌")}
            </Button>
          </div>
        }
      >
        <div className="space-y-3">
          <Field label={t("令牌名称")}>
            <Input
              value={tokenName}
              onChange={(e) => setTokenName(e.target.value)}
              placeholder={t("例如：Chrome 油猴脚本、iOS 快捷指令、RSS 阅读器")}
            />
          </Field>
          <Field label={t("有效天数 (留空为永不过期)")}>
            <NumberInput
              min={1}
              max={3650}
              value={expiresInDays}
              onChange={setExpiresInDays}
              placeholder={t("永不过期")}
              addonAfter={t("天")}
            />
          </Field>
        </div>
      </Modal>
    </section>
  );
}
