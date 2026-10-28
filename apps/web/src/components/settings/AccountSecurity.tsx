import { KeyRound } from "lucide-react";
import React, { useContext, useState } from "react";
import { changeAccountPassword } from "../../lib/api";
import { invalidateSession } from "../../lib/session";
import { AccountContext } from "../auth/AuthGate";
import { Button } from "../ui/Button";
import { Field, Input } from "../ui/Input";

export function AccountSecurity() {
  const username = useContext(AccountContext);
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (pending) return;
    setError("");
    if (newPassword !== confirmation) {
      setError("两次输入的密码不一致");
      return;
    }
    setPending(true);
    try {
      await changeAccountPassword(currentPassword, newPassword);
      invalidateSession();
    } catch (error) {
      setError(error instanceof Error ? error.message : "修改密码失败");
    } finally { setPending(false); }
  }

  return (
    <section className="border-t border-line pt-6">
      <h2 className="text-base font-semibold text-fg">账号安全</h2>
      <p className="mt-2 break-all text-sm text-fg-muted">{username}</p>
      <form onSubmit={submit} className="mt-4 grid max-w-xl gap-4 sm:grid-cols-2">
        <input type="text" autoComplete="username" value={username} readOnly hidden />
        <Field label="当前密码" className="sm:col-span-2">
          <Input type="password" autoComplete="current-password" value={currentPassword} onChange={(e) => setCurrentPassword(e.target.value)} required disabled={pending} />
        </Field>
        <Field label="新密码">
          <Input type="password" autoComplete="new-password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} minLength={8} required disabled={pending} />
        </Field>
        <Field label="确认新密码">
          <Input type="password" autoComplete="new-password" value={confirmation} onChange={(e) => setConfirmation(e.target.value)} minLength={8} required disabled={pending} />
        </Field>
        {error && <p role="alert" className="break-words text-sm text-danger-strong sm:col-span-2">{error}</p>}
        <div className="sm:col-span-2">
          <Button type="submit" icon={<KeyRound className="size-4" />} loading={pending}>修改密码</Button>
        </div>
      </form>
    </section>
  );
}
