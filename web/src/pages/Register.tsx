import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { SendCodeField } from "@/components/auth/SendCodeField";
import { LOGIN_NOTICE_KEY, clearLoginReturnTo } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { registerUser, sendEmailCode } from "@/lib/iam-api";

export default function Register() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const inviteCodeFromLink = useMemo(() => {
    return new URLSearchParams(location.searchStr).get("aff")?.trim() || "";
  }, [location.searchStr]);
  const [email, setEmail] = useState("");
  const [nickname, setNickname] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [inviteCode, setInviteCode] = useState("");
  const [emailCode, setEmailCode] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (inviteCodeFromLink) {
      setInviteCode(inviteCodeFromLink);
    }
  }, [inviteCodeFromLink]);

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (password !== confirmPassword) {
      setError(t("Passwords do not match."));
      return;
    }

    setSubmitting(true);
    setError("");
    setNotice("");

    try {
      await registerUser({
        email: email.trim(),
        nickname: nickname.trim() || undefined,
        password,
        code: emailCode.trim(),
        inviteCode: inviteCode.trim() || undefined,
      });
      sessionStorage.setItem(LOGIN_NOTICE_KEY, "Registration completed. Please log in.");
      clearLoginReturnTo();
      void navigate({ to: "/login" });
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, "Registration failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="flex min-h-[calc(100svh-64px)] items-center justify-center bg-[var(--canvas)] px-4">
      <div className="w-full max-w-md rounded-xl border border-[var(--divider)] bg-[var(--surface)] p-8 shadow-sm">
        <div className="mb-8 flex flex-col items-center gap-2">
          <img src="/logo.png" alt="Remail" className="h-12 w-12" />
          <h1 className="text-xl font-bold text-[var(--ink-primary)]">Remail</h1>
          <p className="text-sm text-[var(--ink-muted)]">{t("Create your account")}</p>
        </div>
        {error ? (
          <div className="mb-4 rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">
            {error}
          </div>
        ) : null}
        {notice ? (
          <div className="mb-4 rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">
            {notice}
          </div>
        ) : null}
        <form className="space-y-4" onSubmit={handleSubmit}>
          <input
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder={t("Email")}
            className="input-antd w-full"
            autoComplete="email"
            required
          />
          <SendCodeField
            email={email}
            code={emailCode}
            onCodeChange={setEmailCode}
            send={sendEmailCode}
            disabled={submitting}
            onNotice={setNotice}
            onError={setError}
          />
          <input
            type="text"
            value={nickname}
            onChange={(event) => setNickname(event.target.value)}
            placeholder={t("Nickname optional")}
            className="input-antd w-full"
            autoComplete="nickname"
          />
          <input
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            placeholder={t("Password")}
            className="input-antd w-full"
            autoComplete="new-password"
            minLength={6}
            required
          />
          <input
            type="password"
            value={confirmPassword}
            onChange={(event) => setConfirmPassword(event.target.value)}
            placeholder={t("Confirm password")}
            className="input-antd w-full"
            autoComplete="new-password"
            minLength={6}
            required
          />
          {inviteCodeFromLink ? null : (
            <input
              type="text"
              value={inviteCode}
              onChange={(event) => setInviteCode(event.target.value)}
              placeholder={t("Invite code optional")}
              className="input-antd w-full"
              autoComplete="off"
            />
          )}
          <button
            className="flex h-10 w-full items-center justify-center rounded-lg bg-gradient-to-br from-[var(--brand-start)] to-[var(--brand-end)] text-[14px] font-semibold text-white shadow-sm transition-all hover:shadow-md disabled:cursor-not-allowed disabled:opacity-70"
            disabled={submitting}
          >
            {submitting ? <Loader2 className="mr-2 size-4 animate-spin" /> : null}
            {t("Register")}
          </button>
        </form>
        <div className="mt-5 text-center text-sm text-[var(--ink-muted)]">
          {t("Already have an account")}{" "}
          <Link
            to="/login"
            onClick={clearLoginReturnTo}
            className="font-medium text-brand hover:text-brand-hover"
          >
            {t("Login")}
          </Link>
        </div>
      </div>
    </div>
  );
}
