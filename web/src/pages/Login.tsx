import { Link, useNavigate } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { CaptchaField } from "@/components/auth/CaptchaField";
import { useAuth } from "@/context/auth-provider";
import { useCaptcha } from "@/hooks/use-captcha";
import { LOGIN_NOTICE_KEY } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";

export default function Login() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { login } = useAuth();
  const captcha = useCaptcha();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [captchaAnswer, setCaptchaAnswer] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    const nextNotice = sessionStorage.getItem(LOGIN_NOTICE_KEY);
    if (nextNotice) {
      setNotice(nextNotice);
      sessionStorage.removeItem(LOGIN_NOTICE_KEY);
    }
  }, []);

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!captcha.captcha?.captchaId) {
      setError(t("Captcha is not ready."));
      return;
    }

    setSubmitting(true);
    setError("");
    setNotice("");

    try {
      await login({
        email: email.trim(),
        password,
        captchaId: captcha.captcha.captchaId,
        captchaAnswer: captchaAnswer.trim(),
      });
      void navigate({ to: "/dashboard" });
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, "Login failed."));
      setCaptchaAnswer("");
      void captcha.refresh();
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="flex min-h-[calc(100svh-64px)] items-center justify-center bg-[var(--canvas)] px-4">
      <div className="w-full max-w-sm rounded-xl border border-[var(--divider)] bg-[var(--surface)] p-8 shadow-sm">
        <div className="mb-8 flex flex-col items-center gap-2">
          <img src="/logo.png" alt="Remail" className="h-12 w-12" />
          <h1 className="text-xl font-bold text-[var(--ink-primary)]">Remail</h1>
          <p className="text-sm text-[var(--ink-muted)]">{t("Log in to your account")}</p>
        </div>
        {notice ? (
          <div className="mb-4 rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">
            {t(notice)}
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
          <input
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            placeholder={t("Password")}
            className="input-antd w-full"
            autoComplete="current-password"
            required
          />
          <CaptchaField
            captcha={captcha.captcha}
            loading={captcha.loading}
            value={captchaAnswer}
            disabled={submitting}
            onChange={setCaptchaAnswer}
            onRefresh={() => void captcha.refresh()}
          />
          <button
            className="flex h-10 w-full items-center justify-center rounded-lg bg-gradient-to-br from-[var(--brand-start)] to-[var(--brand-end)] text-[14px] font-semibold text-white shadow-sm transition-all hover:shadow-md disabled:cursor-not-allowed disabled:opacity-70"
            disabled={submitting || captcha.loading}
          >
            {submitting ? <Loader2 className="mr-2 size-4 animate-spin" /> : null}
            {t("Login")}
          </button>
        </form>
        <div className="mt-5 flex flex-col items-center gap-2 text-sm text-[var(--ink-muted)]">
          <Link to="/password-reset" className="font-medium text-brand hover:text-brand-hover">
            {t("Forgot password")}
          </Link>
          <div>
            {t("No account yet")}{" "}
            <Link to="/register" className="font-medium text-brand hover:text-brand-hover">
              {t("Register")}
            </Link>
          </div>
        </div>
      </div>
    </div>
  );
}
