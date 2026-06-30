import { Link, useNavigate } from "@tanstack/react-router";
import { Loader2, MailCheck } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { CaptchaField } from "@/components/auth/CaptchaField";
import { useCaptcha } from "@/hooks/use-captcha";
import { LOGIN_NOTICE_KEY } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { requestPasswordReset, resetPassword } from "@/lib/iam-api";

export default function PasswordReset() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const captcha = useCaptcha();
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [captchaAnswer, setCaptchaAnswer] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [requestingCode, setRequestingCode] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const handleRequestCode = async () => {
    if (!email.trim()) {
      setError(t("Please enter your email."));
      return;
    }
    if (!captcha.captcha?.captchaId) {
      setError(t("Captcha is not ready."));
      return;
    }
    if (!captchaAnswer.trim()) {
      setError(t("Please enter captcha."));
      return;
    }

    setRequestingCode(true);
    setError("");
    setNotice("");

    try {
      await requestPasswordReset({
        email: email.trim(),
        captchaId: captcha.captcha.captchaId,
        captchaAnswer: captchaAnswer.trim(),
      });
      setNotice(t("Verification code sent."));
      setCaptchaAnswer("");
      void captcha.refresh();
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, "Failed to send verification code."));
      setCaptchaAnswer("");
      void captcha.refresh();
    } finally {
      setRequestingCode(false);
    }
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (newPassword !== confirmPassword) {
      setError(t("Passwords do not match."));
      return;
    }

    setSubmitting(true);
    setError("");

    try {
      await resetPassword({
        email: email.trim(),
        code: code.trim(),
        newPassword,
      });
      sessionStorage.setItem(LOGIN_NOTICE_KEY, "Password reset completed. Please log in.");
      void navigate({ to: "/login", replace: true });
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, "Password reset failed."));
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
          <p className="text-sm text-[var(--ink-muted)]">{t("Reset your password")}</p>
        </div>

        {notice ? (
          <div className="mb-4 flex items-center gap-2 rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">
            <MailCheck className="size-4" />
            {notice}
          </div>
        ) : null}
        {error ? (
          <div className="mb-4 rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">
            {error}
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
          <CaptchaField
            captcha={captcha.captcha}
            loading={captcha.loading}
            value={captchaAnswer}
            disabled={submitting || requestingCode}
            onChange={setCaptchaAnswer}
            onRefresh={() => void captcha.refresh()}
          />
          <div className="grid grid-cols-[1fr_112px] gap-2">
            <input
              type="text"
              value={code}
              onChange={(event) => setCode(event.target.value)}
              placeholder={t("Verification code")}
              className="input-antd min-w-0 w-full"
              autoComplete="one-time-code"
              required
            />
            <button
              type="button"
              onClick={() => void handleRequestCode()}
              disabled={submitting || requestingCode || captcha.loading}
              className="h-9 w-28 rounded-lg border border-[var(--divider)] px-3 text-sm font-medium text-[var(--ink-secondary)] transition-colors hover:bg-[var(--surface-hover)] disabled:cursor-not-allowed disabled:opacity-70"
            >
              {requestingCode ? <Loader2 className="size-4 animate-spin" /> : t("Send code")}
            </button>
          </div>
          <input
            type="password"
            value={newPassword}
            onChange={(event) => setNewPassword(event.target.value)}
            placeholder={t("New password")}
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
          <button
            className="flex h-10 w-full items-center justify-center rounded-lg bg-gradient-to-br from-[var(--brand-start)] to-[var(--brand-end)] text-[14px] font-semibold text-white shadow-sm transition-all hover:shadow-md disabled:cursor-not-allowed disabled:opacity-70"
            disabled={submitting}
          >
            {submitting ? <Loader2 className="mr-2 size-4 animate-spin" /> : null}
            {t("Reset password")}
          </button>
        </form>

        <div className="mt-5 text-center text-sm text-[var(--ink-muted)]">
          <Link to="/login" className="font-medium text-brand hover:text-brand-hover">
            {t("Back to login")}
          </Link>
        </div>
      </div>
    </div>
  );
}
