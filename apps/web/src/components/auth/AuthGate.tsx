import { t, useTranslation } from "../../lib/i18n";
import { useQueryClient } from "@tanstack/react-query";
import { LogIn, RefreshCw, UserPlus } from "lucide-react";
import React, { createContext, useCallback, useEffect, useRef, useState } from "react";
import { ApiError, getLoginSession, loginAccount, type LoginSession } from "../../lib/api";
import { noteSessionChange, sessionChangedKey, sessionInvalidatedEvent } from "../../lib/session";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { Field, Input } from "../semi/controls";
import { Spinner } from "../semi/feedback";

export const AccountContext = createContext("");

let nextInvalidationIntentional = false;
export function markIntentionalSessionInvalidation() {
  nextInvalidationIntentional = true;
}

export function AuthGate({ children }: { children: React.ReactNode }) {
  useTranslation();
  const queryClient = useQueryClient();
  const [session, setSession] = useState<LoginSession | null>(null);
  const sessionRef = useRef<LoginSession | null>(null);
  sessionRef.current = session;
  const [loadError, setLoadError] = useState("");
  const [notice, setNotice] = useState("");
  const sessionCheck = useRef(0);

  const checkSession = useCallback(async () => {
    const check = ++sessionCheck.current;
    setLoadError("");
    try {
      const current = await getLoginSession();
      if (check !== sessionCheck.current) return;
      if (!current.authenticated) {
        queryClient.clear();
      } else {
        setNotice("");
      }
      setSession(current);
    } catch (error) {
      if (check !== sessionCheck.current) return;
      if (!sessionRef.current) {
        setLoadError(error instanceof Error ? error.message : t("无法连接服务器"));
      }
    }
  }, [queryClient]);

  useEffect(() => {
    void checkSession();
    const invalidated = (event: Event) => {
      const detail = (event as CustomEvent<{ intentional?: boolean; silent?: boolean }> | undefined)?.detail;
      const intentional = nextInvalidationIntentional || Boolean(detail?.intentional || detail?.silent);
      nextInvalidationIntentional = false;
      sessionCheck.current += 1;
      queryClient.clear();
      setSession({ configured: true, authenticated: false });
      setLoadError("");
      setNotice(intentional ? "" : t("登录已失效，请重新登录"));
    };
    const storageChanged = (event: StorageEvent) => {
      if (event.key === sessionChangedKey) {
        noteSessionChange(false);
        void checkSession();
      }
    };
    window.addEventListener(sessionInvalidatedEvent, invalidated);
    window.addEventListener("storage", storageChanged);
    return () => {
      sessionCheck.current += 1;
      window.removeEventListener(sessionInvalidatedEvent, invalidated);
      window.removeEventListener("storage", storageChanged);
    };
  }, [checkSession, queryClient]);

  useEffect(() => {
    if (!session?.authenticated) return;
    const handleFocus = () => {
      void checkSession();
    };
    window.addEventListener("focus", handleFocus);
    return () => {
      window.removeEventListener("focus", handleFocus);
    };
  }, [checkSession, session?.authenticated]);

  if (session?.authenticated) {
    return <AccountContext.Provider value={session.username ?? ""}>{children}</AccountContext.Provider>;
  }

  return (
    <main className="auth-page flex min-h-dvh items-center justify-center bg-canvas px-4 py-10">
      <div className="auth-card">
        <div className="mb-7 flex items-center gap-3">
          <img src="/icon.svg" alt="" className="size-10 shrink-0" />
          <div className="min-w-0">
            <span className="block min-w-0 break-words text-xl font-semibold leading-7 text-fg">open-Xdownload</span>
            <p className="mt-0.5 text-xs text-fg-muted">{t("推文多媒体归档工作台")}</p>
          </div>
        </div>
        {loadError ? (
          <div className="space-y-4">
            <p role="alert" className="break-words text-sm text-danger-strong">{loadError}</p>
            <Button type="tertiary" icon={<RefreshCw className="size-4" />} onClick={() => void checkSession()}>{t("重试")}</Button>
          </div>
        ) : session ? (
          <LoginForm
            setup={!session.configured}
            notice={notice}
            onConfigured={() => setSession({ configured: true, authenticated: false })}
            onLogin={(username) => {
              nextInvalidationIntentional = false;
              sessionCheck.current += 1;
              noteSessionChange();
              queryClient.clear();
              setNotice("");
              setSession({ configured: true, authenticated: true, username });
            }}
          />
        ) : (
          <div role="status" aria-label={t("正在检查登录状态")} className="flex justify-center py-8">
            <Spinner className="size-5 text-brand-500" />
          </div>
        )}
      </div>
    </main>
  );
}

function LoginForm({ setup, notice, onLogin, onConfigured }: {
  setup: boolean;
  notice: string;
  onLogin: (username: string) => void;
  onConfigured: () => void;
}) {
  useTranslation();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (pending) return;
    setError("");
    const trimmedUsername = username.trim();
    if (!trimmedUsername) {
      setUsername("");
      return;
    }
    if (setup && password !== confirmation) {
      setError(t("两次输入的密码不一致"));
      return;
    }
    setPending(true);
    try {
      const result = await loginAccount(trimmedUsername, password, setup);
      onLogin(result.username);
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        setConfirmation("");
        onConfigured();
      }
      setError(error instanceof Error ? error.message : t("登录失败"));
    } finally { setPending(false); }
  }

  return (
    <form onSubmit={submit} className="space-y-5">
      <h1 className="text-lg font-semibold text-fg">{setup ? t("创建账号") : t("登录")}</h1>
      {notice && <p role="status" className="rounded-control bg-surface-muted px-3 py-2 text-xs text-fg-muted">{notice}</p>}
      <Field label={t("用户名")}>
        <Input autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required maxLength={64} autoFocus disabled={pending} />
      </Field>
      <Field label={t("密码")}>
        <Input key={setup ? "setup" : "login"} type="password" autoComplete={setup ? "new-password" : "current-password"} value={password} onChange={(e) => setPassword(e.target.value)} minLength={setup ? 8 : undefined} required disabled={pending} />
      </Field>
      {setup && <Field label={t("确认密码")}>
        <Input type="password" autoComplete="new-password" value={confirmation} onChange={(e) => setConfirmation(e.target.value)} minLength={8} required disabled={pending} />
      </Field>}
      {error && <p role="alert" className="break-words text-sm text-danger-strong">{error}</p>}
      <Button htmlType="submit" theme="solid" block loading={pending} disabled={pending} aria-busy={pending} icon={setup ? <UserPlus className="size-4" /> : <LogIn className="size-4" />}>
        {setup ? t("创建账号") : t("登录")}
      </Button>
    </form>
  );
}
