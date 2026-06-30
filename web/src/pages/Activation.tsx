import { useNavigate } from "@tanstack/react-router";
import { CheckCircle2, Loader2, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { LanguageMenu } from "@/components/language-menu";
import { useActivationGate } from "@/context/activation-gate";
import { LOGIN_NOTICE_KEY } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { activateSystem, IamApiError } from "@/lib/iam-api";

const LOGIN_REDIRECT_DELAY_MS = 900;
const ACTIVATION_COMPLETED_MESSAGE = "Activation has already been completed.";

export default function Activation() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { activationNeeded, markActivated } = useActivationGate();
  const [email, setEmail] = useState("");
  const [nickname, setNickname] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [completed, setCompleted] = useState(false);
  const [success, setSuccess] = useState("");
  const [error, setError] = useState("");
  const redirectTimer = useRef<number | null>(null);

  useEffect(() => {
    return () => {
      if (redirectTimer.current) {
        window.clearTimeout(redirectTimer.current);
      }
    };
  }, []);

  useEffect(() => {
    if (activationNeeded === false) {
      void navigate({ to: "/login", replace: true });
    }
  }, [activationNeeded, navigate]);

  const scheduleLoginRedirect = () => {
    setCompleted(true);
    setSubmitting(false);
    setError("");
    setSuccess(t("System activated. Redirecting to login."));
    sessionStorage.setItem(LOGIN_NOTICE_KEY, "System activated. Please log in.");

    redirectTimer.current = window.setTimeout(() => {
      markActivated();
      void navigate({ to: "/login", replace: true });
    }, LOGIN_REDIRECT_DELAY_MS);
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (submitting || completed) return;

    if (password !== confirmPassword) {
      setError(t("Passwords do not match."));
      return;
    }

    setSubmitting(true);
    setSuccess("");
    setError("");

    try {
      await activateSystem({
        email: email.trim(),
        nickname: nickname.trim() || undefined,
        password,
      });
      scheduleLoginRedirect();
    } catch (nextError) {
      if (isActivationCompleted(nextError)) {
        scheduleLoginRedirect();
        return;
      }
      setError(getIamErrorMessage(t, nextError, "Activation failed."));
      setSubmitting(false);
    } finally {
      if (!completed) {
        setSubmitting(false);
      }
    }
  };

  const controlsDisabled = submitting || completed;

  return (
    <div className="min-h-svh bg-[var(--canvas)] px-4 py-8">
      <div className="absolute right-4 top-4">
        <LanguageMenu />
      </div>

      <div className="mx-auto flex min-h-[calc(100svh-64px)] w-full max-w-3xl flex-col justify-center gap-6 sm:min-h-[calc(100svh-80px)]">
        <div className="flex flex-col items-center gap-3 text-center">
          <img src="/logo.png" alt="Remail" className="h-12 w-12 rounded-lg" />
          <div>
            <h1 className="text-2xl font-semibold tracking-tight text-[var(--ink-primary)]">
              {t("Initialize Remail")}
            </h1>
            <p className="mt-2 text-sm text-[var(--ink-muted)]">
              {t("Create the first super administrator before the first login.")}
            </p>
          </div>
        </div>

        <div className="rounded-xl border border-[var(--divider)] bg-[var(--surface)] shadow-sm">
          <div className="border-b border-[var(--divider)] px-6 py-5">
            <div className="flex items-center gap-3">
              <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-brand-subtle text-brand">
                <ShieldCheck className="size-5" />
              </span>
              <div>
                <h2 className="text-base font-semibold text-[var(--ink-primary)]">
                  {t("First activation")}
                </h2>
                <p className="text-sm text-[var(--ink-muted)]">
                  {t("Activation is allowed only while the user table is empty.")}
                </p>
              </div>
            </div>
          </div>

          <div className="px-6 py-5">
            {activationNeeded === null ? (
              <div className="flex h-48 items-center justify-center text-[var(--ink-muted)]">
                <Loader2 className="mr-2 size-4 animate-spin" />
                {t("Checking activation status")}
              </div>
            ) : (
              <>
                {error ? (
                  <div className="mb-4 rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">
                    {error}
                  </div>
                ) : null}

                {success ? (
                  <div className="mb-4 rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">
                    {success}
                  </div>
                ) : null}

                <form className="grid gap-4 sm:grid-cols-2" onSubmit={handleSubmit}>
                  <input
                    type="email"
                    value={email}
                    onChange={(event) => setEmail(event.target.value)}
                    placeholder={t("Administrator email")}
                    className="input-antd w-full"
                    autoComplete="email"
                    disabled={controlsDisabled}
                    required
                  />
                  <input
                    type="text"
                    value={nickname}
                    onChange={(event) => setNickname(event.target.value)}
                    placeholder={t("Nickname optional")}
                    className="input-antd w-full"
                    autoComplete="nickname"
                    disabled={controlsDisabled}
                  />
                  <input
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    placeholder={t("Password")}
                    className="input-antd w-full"
                    autoComplete="new-password"
                    minLength={6}
                    disabled={controlsDisabled}
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
                    disabled={controlsDisabled}
                    required
                  />
                  <button
                    className="flex h-10 items-center justify-center rounded-lg bg-gradient-to-br from-[var(--brand-start)] to-[var(--brand-end)] text-sm font-semibold text-white shadow-sm transition-all hover:shadow-md disabled:cursor-not-allowed disabled:opacity-70 sm:col-span-2"
                    disabled={controlsDisabled}
                  >
                    {controlsDisabled ? (
                      <Loader2 className="mr-2 size-4 animate-spin" />
                    ) : (
                      <CheckCircle2 className="mr-2 size-4" />
                    )}
                    {completed
                      ? t("Redirecting to login")
                      : submitting
                        ? t("Activating system")
                        : t("Activate system")}
                  </button>
                </form>
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function isActivationCompleted(error: unknown) {
  return (
    error instanceof IamApiError &&
    error.status === 409 &&
    error.message === ACTIVATION_COMPLETED_MESSAGE
  );
}
