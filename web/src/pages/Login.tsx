import { Link, useNavigate } from "@tanstack/react-router";
import { Button, Modal, Radio, RadioGroup } from "@douyinfe/semi-ui";
import { Loader2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { SendCodeField } from "@/components/auth/SendCodeField";
import { TurnstileField } from "@/components/auth/TurnstileField";
import { LinuxDoIcon } from "@/components/auth/LinuxDoIcon";
import { useAuth } from "@/context/auth-provider";
import { LOGIN_NOTICE_KEY, consumeLoginReturnTo } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  completeLinuxDO,
  getLinuxDOPending,
  getLoginConfig,
  linuxDOLoginURL,
  sendLinuxDOEmailCode,
  type LinuxDOAccountMode,
  type LinuxDOPendingResponse,
} from "@/lib/iam-api";

const linuxDOErrorKeys: Record<string, string> = {
  account: "LinuxDO account is unavailable.",
  cancelled: "LinuxDO authorization was cancelled.",
  disabled: "LinuxDO login is unavailable.",
  failed: "LinuxDO login failed.",
  rate_limited: "Too many LinuxDO login attempts. Please try again later.",
  registration_disabled: "Registration is disabled.",
  session: "Your session expired. Please log in and try again.",
  state: "LinuxDO login request expired. Please try again.",
  trust_level: "Your LinuxDO trust level is too low.",
};

type LinuxDOConfigState = "loading" | "enabled" | "disabled" | "error";

function removeSearchParam(name: string) {
  const params = new URLSearchParams(window.location.search);
  params.delete(name);
  const search = params.toString();
  window.history.replaceState(
    {},
    "",
    window.location.pathname + (search ? `?${search}` : "") + window.location.hash
  );
}

export default function Login() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { login, refreshCurrentUser } = useAuth();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [turnstileToken, setTurnstileToken] = useState("");
  const [turnstileResetKey, setTurnstileResetKey] = useState(0);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [linuxDOConfigState, setLinuxDOConfigState] = useState<LinuxDOConfigState>("loading");
  const [linuxDOPending, setLinuxDOPending] = useState<LinuxDOPendingResponse | null>(null);
  const [linuxDOPendingLoading, setLinuxDOPendingLoading] = useState(false);
  const [linuxDOMode, setLinuxDOMode] = useState<LinuxDOAccountMode>("new");
  const [linuxDOEmail, setLinuxDOEmail] = useState("");
  const [linuxDOCode, setLinuxDOCode] = useState("");
  const [linuxDOSetupError, setLinuxDOSetupError] = useState("");
  const [linuxDOSetupNotice, setLinuxDOSetupNotice] = useState("");
  const [linuxDOCompleting, setLinuxDOCompleting] = useState(false);

  useEffect(() => {
    const nextNotice = sessionStorage.getItem(LOGIN_NOTICE_KEY);
    if (nextNotice) {
      setNotice(nextNotice);
      sessionStorage.removeItem(LOGIN_NOTICE_KEY);
    }

    const params = new URLSearchParams(window.location.search);
    const oauthError = params.get("oauth_error");
    if (oauthError) {
      setError(t(Object.prototype.hasOwnProperty.call(linuxDOErrorKeys, oauthError) ? linuxDOErrorKeys[oauthError] : "LinuxDO login failed."));
      params.delete("oauth_error");
      const search = params.toString();
      window.history.replaceState({}, "", window.location.pathname + (search ? "?" + search : "") + window.location.hash);
    }

    let cancelled = false;
    if (params.get("oauth_setup") === "linuxdo") {
      setLinuxDOPendingLoading(true);
      void getLinuxDOPending()
        .then((pending) => {
          if (cancelled) return;
          setLinuxDOPending(pending);
          setLinuxDOMode(
            pending.legacyAccount
              ? "new"
              : pending.suggestedEmailExists || !pending.registrationEnabled
              ? "existing"
              : "new"
          );
          setLinuxDOEmail(
            pending.legacyAccount && pending.suggestedEmailExists
              ? ""
              : pending.suggestedEmail
          );
        })
        .catch((nextError) => {
          if (cancelled) return;
          setError(
            getIamErrorMessage(
              t,
              nextError,
              "LinuxDO account setup expired. Please sign in with LinuxDO again."
            )
          );
          removeSearchParam("oauth_setup");
        })
        .finally(() => {
          if (!cancelled) setLinuxDOPendingLoading(false);
        });
    }
    void getLoginConfig()
      .then((config) => {
        if (!cancelled) setLinuxDOConfigState(config.linuxdoOAuthEnabled ? "enabled" : "disabled");
      })
      .catch(() => {
        if (!cancelled) setLinuxDOConfigState("error");
      });
    return () => {
      cancelled = true;
    };
  }, [t]);

  const closeLinuxDOSetup = () => {
    setLinuxDOPending(null);
    setLinuxDOCode("");
    setLinuxDOSetupError("");
    setLinuxDOSetupNotice("");
    removeSearchParam("oauth_setup");
  };

  const handleLinuxDOComplete = async () => {
    if (!linuxDOEmail.trim()) {
      setLinuxDOSetupError(t("Please enter your email."));
      return;
    }
    if (!linuxDOCode.trim()) {
      setLinuxDOSetupError(t("Please enter the verification code."));
      return;
    }
    setLinuxDOCompleting(true);
    setLinuxDOSetupError("");
    setLinuxDOSetupNotice("");
    try {
      await completeLinuxDO({
        mode: linuxDOMode,
        email: linuxDOEmail.trim(),
        code: linuxDOCode.trim(),
      });
      await refreshCurrentUser();
      void navigate({ to: consumeLoginReturnTo() as never, replace: true });
    } catch (nextError) {
      setLinuxDOSetupError(
        getIamErrorMessage(t, nextError, "LinuxDO account setup failed.")
      );
    } finally {
      setLinuxDOCompleting(false);
    }
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!turnstileToken) {
      setError(t("Please complete human verification."));
      return;
    }

    setSubmitting(true);
    setError("");
    setNotice("");

    try {
      await login({
        email: email.trim(),
        password,
        turnstileToken,
      });
      void navigate({ to: consumeLoginReturnTo() as never, replace: true });
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, "Login failed."));
      setTurnstileToken("");
      setTurnstileResetKey((key) => key + 1);
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
          <div role="status" aria-live="polite" className="mb-4 rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">
            {t(notice)}
          </div>
        ) : null}
        {linuxDOPendingLoading ? (
          <div role="status" aria-live="polite" className="mb-4 flex items-center gap-2 rounded-lg border border-[var(--divider)] px-3 py-2 text-sm text-[var(--ink-muted)]">
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            {t("Preparing LinuxDO account setup...")}
          </div>
        ) : null}
        {error ? (
          <div role="alert" aria-live="assertive" className="mb-4 rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">
            {error}
          </div>
        ) : null}
        {linuxDOConfigState === "enabled" || linuxDOConfigState === "loading" ? (
          <div className="mb-5 space-y-4">
            <button
              type="button"
              aria-busy={linuxDOConfigState === "loading"}
              disabled={linuxDOConfigState === "loading"}
              onClick={() => window.location.assign(linuxDOLoginURL)}
              className="flex h-11 w-full cursor-pointer items-center justify-center gap-2 rounded-lg border border-[var(--divider)] bg-[var(--surface)] px-4 text-sm font-semibold text-[var(--ink-primary)] transition-colors duration-200 hover:bg-[var(--surface-sunken)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--brand-start)] focus-visible:ring-offset-2 disabled:cursor-wait disabled:opacity-70"
            >
              {linuxDOConfigState === "loading" ? <Loader2 className="size-5 animate-spin" aria-hidden="true" /> : <LinuxDoIcon className="size-5" />}
              {t(linuxDOConfigState === "loading" ? "Loading LinuxDO login..." : "Continue with LinuxDO")}
            </button>
            <div className="flex items-center gap-3" aria-hidden="true">
              <span className="h-px flex-1 bg-[var(--divider)]" />
              <span className="text-xs uppercase tracking-wide text-[var(--ink-muted)]">{t("or")}</span>
              <span className="h-px flex-1 bg-[var(--divider)]" />
            </div>
          </div>
        ) : linuxDOConfigState === "error" ? (
          <div role="alert" aria-live="polite" className="mb-5 rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">
            {t("Could not load LinuxDO login settings. Please refresh and try again.")}
          </div>
        ) : null}
        <form className="space-y-4" onSubmit={handleSubmit}>
          <label htmlFor="login-email" className="sr-only">{t("Email")}</label>
          <input
            id="login-email"
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder={t("Email")}
            className="input-antd h-11 w-full"
            autoComplete="email"
            required
          />
          <label htmlFor="login-password" className="sr-only">{t("Password")}</label>
          <input
            id="login-password"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            placeholder={t("Password")}
            className="input-antd h-11 w-full"
            autoComplete="current-password"
            required
          />
          <TurnstileField
            action="login"
            resetKey={turnstileResetKey}
            onTokenChange={setTurnstileToken}
          />
          <button
            className="flex h-11 w-full cursor-pointer items-center justify-center rounded-lg bg-gradient-to-br from-[var(--brand-start)] to-[var(--brand-end)] text-[14px] font-semibold text-white shadow-sm transition-shadow duration-200 hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--brand-start)] focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-70"
            disabled={submitting || !turnstileToken}
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

      <Modal
        centered
        footer={
          <div className="flex justify-end gap-2">
            <Button disabled={linuxDOCompleting} onClick={closeLinuxDOSetup} theme="light" type="tertiary">
              {t("Cancel")}
            </Button>
            <Button loading={linuxDOCompleting} onClick={() => void handleLinuxDOComplete()} theme="solid" type="primary">
              {t("Continue")}
            </Button>
          </div>
        }
        maskClosable={false}
        onCancel={closeLinuxDOSetup}
        title={t("Finish LinuxDO sign-in")}
        visible={linuxDOPending !== null}
        width="min(480px, calc(100vw - 32px))"
      >
        {linuxDOPending ? (
          <div className="space-y-4 py-1">
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm text-[var(--semi-color-text-1)]">
              <div className="font-medium text-[var(--semi-color-text-0)]">
                {linuxDOPending.username}
              </div>
              <div className="mt-0.5 text-xs">LinuxDO ID: {linuxDOPending.providerUserId}</div>
            </div>

            <div>
              <div className="mb-2 text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Choose account ownership")}
              </div>
              <RadioGroup
                type="button"
                value={linuxDOMode}
                onChange={(event) => {
                  setLinuxDOMode(event.target.value as LinuxDOAccountMode);
                  setLinuxDOCode("");
                  setLinuxDOSetupError("");
                  setLinuxDOSetupNotice("");
                }}
              >
                <Radio disabled={linuxDOPending.legacyAccount} value="existing">
                  {t("Bind existing account")}
                </Radio>
                <Radio
                  disabled={!linuxDOPending.registrationEnabled && !linuxDOPending.legacyAccount}
                  value="new"
                >
                  {t(linuxDOPending.legacyAccount ? "Upgrade current account" : "Create new account")}
                </Radio>
              </RadioGroup>
              <p className="mt-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
                {t(
                  linuxDOPending.legacyAccount
                    ? "This legacy LinuxDO account already contains site data. Verify a new email to keep this account, balance, orders, and resources."
                    : linuxDOMode === "existing"
                    ? "Verify the email of your existing account. Its email and password will not be changed."
                    : "Verify a receiving email to create an account. No password is set; use Forgot password later if needed."
                )}
              </p>
            </div>

            {linuxDOSetupNotice ? (
              <div role="status" aria-live="polite" className="rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">
                {linuxDOSetupNotice}
              </div>
            ) : null}
            {linuxDOSetupError ? (
              <div role="alert" aria-live="assertive" className="rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">
                {linuxDOSetupError}
              </div>
            ) : null}

            <label htmlFor="linuxdo-account-email" className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Email")}
              </span>
              <input
                id="linuxdo-account-email"
                autoComplete="email"
                className="input-antd h-11 w-full"
                disabled={linuxDOCompleting}
                onChange={(event) => {
                  setLinuxDOEmail(event.target.value);
                  setLinuxDOCode("");
                }}
                placeholder={t("Email")}
                required
                type="email"
                value={linuxDOEmail}
              />
            </label>

            <SendCodeField
              code={linuxDOCode}
              disabled={linuxDOCompleting}
              email={linuxDOEmail}
              onCodeChange={setLinuxDOCode}
              onError={setLinuxDOSetupError}
              onNotice={setLinuxDOSetupNotice}
              send={(payload) =>
                sendLinuxDOEmailCode({ ...payload, mode: linuxDOMode })
              }
              turnstileAction="linuxdo_email_code"
            />
          </div>
        ) : null}
      </Modal>
    </div>
  );
}
