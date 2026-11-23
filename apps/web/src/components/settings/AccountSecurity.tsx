import { t, useTranslation } from "../../lib/i18n";
import { KeyRound, LogOut } from "lucide-react";
import React, { useContext, useId, useState } from "react";
import { changeAccountPassword, logoutAccount } from "../../lib/api";
import { invalidateSession } from "../../lib/session";
import { AccountContext, markIntentionalSessionInvalidation } from "../auth/AuthGate";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { Field, Input } from "../semi/controls";
import { Modal } from "../semi/overlays";

export function AccountSecurity({ compact = false }: { compact?: boolean }) {
  useTranslation();
  const username = useContext(AccountContext);
  const formId = useId();
  const [open, setOpen] = useState(false);
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [logoutPending, setLogoutPending] = useState(false);
  const [logoutError, setLogoutError] = useState("");

  function close() {
    if (pending) return;
    setOpen(false);
    setCurrentPassword("");
    setNewPassword("");
    setConfirmation("");
    setError("");
  }

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (pending) return;
    setError("");
    if (newPassword !== confirmation) {
      setError(t("两次输入的密码不一致"));
      return;
    }
    setPending(true);
    try {
      await changeAccountPassword(currentPassword, newPassword);
      markIntentionalSessionInvalidation();
      invalidateSession();
    } catch (error) {
      setError(error instanceof Error ? error.message : t("修改密码失败"));
    } finally { setPending(false); }
  }

  return (
    <section className={`semi-settings-section${compact ? " settings-account-shortcut" : ""}`}>
      <h2 className="settings-section-title"><KeyRound size={16} />{t("账号安全")}</h2>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="min-w-0 break-all text-sm text-fg-muted">{username}</p>
        <div className="flex flex-wrap gap-2">
          <Button type="tertiary" icon={<KeyRound className="size-4" />} disabled={logoutPending} onClick={() => { setError(""); setOpen(true); }}>{t("修改密码")}</Button>
          <Button type="tertiary" icon={<LogOut className="size-4" />} loading={logoutPending} disabled={logoutPending || pending} aria-busy={logoutPending} onClick={async () => {
            setLogoutPending(true);
            setLogoutError("");
            try {
              await logoutAccount();
              markIntentionalSessionInvalidation();
              invalidateSession();
            }
            catch (error) { setLogoutError(error instanceof Error ? error.message : t("退出登录失败")); }
            finally { setLogoutPending(false); }
          }}>{t("退出登录")}</Button>
        </div>
      </div>
      {logoutError && <p role="alert" className="text-sm text-danger-strong">{logoutError}</p>}
      <Modal open={open} onClose={close} title={t("修改密码")} description={t("修改成功后需重新登录。")} footer={<>
        <Button type="tertiary" onClick={close} disabled={pending}>{t("取消")}</Button>
        <Button htmlType="submit" form={formId} theme="solid" icon={<KeyRound className="size-4" />} loading={pending} disabled={pending} aria-busy={pending}>{t("修改密码")}</Button>
      </>}>
        <form id={formId} onSubmit={submit} className="grid gap-4 sm:grid-cols-2">
          <input type="text" autoComplete="username" value={username} readOnly hidden />
          <Field label={t("当前密码")} className="sm:col-span-2">
            <Input type="password" autoComplete="current-password" value={currentPassword} onChange={(e) => setCurrentPassword(e.target.value)} required disabled={pending} />
          </Field>
          <Field label={t("新密码")}>
            <Input type="password" autoComplete="new-password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} minLength={8} required disabled={pending} />
          </Field>
          <Field label={t("确认新密码")}>
            <Input type="password" autoComplete="new-password" value={confirmation} onChange={(e) => setConfirmation(e.target.value)} minLength={8} required disabled={pending} />
          </Field>
          {error && <p role="alert" className="break-words text-sm text-danger-strong sm:col-span-2">{error}</p>}
        </form>
      </Modal>
    </section>
  );
}
