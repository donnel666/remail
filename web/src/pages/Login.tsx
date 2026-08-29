import { Link, useNavigate } from "@tanstack/react-router";
import { IconGithubLogo } from "@douyinfe/semi-icons";
import { Loader2, MapPin } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { EmailOAuthSetupModal } from "@/components/auth/EmailOAuthSetupModal";
import { TurnstileField } from "@/components/auth/TurnstileField";
import { GitHubVerificationModal } from "@/components/auth/GitHubVerificationModal";
import { LinuxDoIcon } from "@/components/auth/LinuxDoIcon";
import { useAuth } from "@/context/auth-provider";
import { LOGIN_NOTICE_KEY, consumeLoginReturnTo } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  completeLinuxDO,
  completeNodeLoc,
  getLinuxDOPending,
  getLoginConfig,
  getNodeLocPending,
  githubLoginURL,
  linuxDOLoginURL,
  nodeLocLoginURL,
  sendLinuxDOEmailCode,
  sendNodeLocEmailCode,
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

const githubErrorKeys: Record<string, string> = {
  account: "GitHub account is unavailable.",
  account_age: "Your GitHub account is too new.",
  already_bound: "This GitHub account is already bound.",
  cancelled: "GitHub authorization was cancelled.",
  disabled: "GitHub login is unavailable.",
  email: "Your GitHub account has no verified email.",
  failed: "GitHub login failed.",
  rate_limited: "Too many GitHub login attempts. Please try again later.",
  registration_disabled: "Registration is disabled.",
  session: "Your session expired. Please log in and try again.",
  state: "GitHub login request expired. Please try again.",
};

const nodeLocErrorKeys: Record<string, string> = {
  account: "NodeLoc account is unavailable.",
  already_bound: "This NodeLoc account is already bound.",
  cancelled: "NodeLoc authorization was cancelled.",
  disabled: "NodeLoc login is unavailable.",
  failed: "NodeLoc login failed.",
  rate_limited: "Too many NodeLoc login attempts. Please try again later.",
  registration_disabled: "Registration is disabled.",
  session: "Your session expired. Please log in and try again.",
  state: "NodeLoc login request expired. Please try again.",
  trust_level: "Your NodeLoc trust level is too low.",
};

type OAuthConfigState = "loading" | "enabled" | "disabled" | "error";

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
  const [linuxDOConfigState, setLinuxDOConfigState] = useState<OAuthConfigState>("loading");
  const [githubConfigState, setGitHubConfigState] = useState<OAuthConfigState>("loading");
  const [nodeLocConfigState, setNodeLocConfigState] = useState<OAuthConfigState>("loading");
  const [githubSetupOpen, setGitHubSetupOpen] = useState(false);
  const [emailOAuthSetup, setEmailOAuthSetup] = useState<"linuxdo" | "nodeloc" | null>(null);

  useEffect(() => {
    const nextNotice = sessionStorage.getItem(LOGIN_NOTICE_KEY);
    if (nextNotice) {
      setNotice(nextNotice);
      sessionStorage.removeItem(LOGIN_NOTICE_KEY);
    }

    const params = new URLSearchParams(window.location.search);
    const oauthError = params.get("oauth_error");
    if (oauthError) {
      const provider = params.get("oauth_provider");
      const errorKeys = provider === "github" ? githubErrorKeys : provider === "nodeloc" ? nodeLocErrorKeys : linuxDOErrorKeys;
      const fallback = provider === "github" ? "GitHub login failed." : provider === "nodeloc" ? "NodeLoc login failed." : "LinuxDO login failed.";
      setError(t(Object.prototype.hasOwnProperty.call(errorKeys, oauthError) ? errorKeys[oauthError] : fallback));
      params.delete("oauth_error");
      params.delete("oauth_provider");
      const search = params.toString();
      window.history.replaceState({}, "", window.location.pathname + (search ? "?" + search : "") + window.location.hash);
    }

    let cancelled = false;
    if (params.get("oauth_setup") === "linuxdo" || params.get("oauth_setup") === "nodeloc") {
      setEmailOAuthSetup(params.get("oauth_setup") as "linuxdo" | "nodeloc");
    }
    if (params.get("oauth_setup") === "github") {
      setGitHubSetupOpen(true);
    }
    void getLoginConfig()
      .then((config) => {
        if (cancelled) return;
        setLinuxDOConfigState(config.linuxdoOAuthEnabled ? "enabled" : "disabled");
        setGitHubConfigState(config.githubOAuthEnabled ? "enabled" : "disabled");
        setNodeLocConfigState(config.nodelocOAuthEnabled ? "enabled" : "disabled");
      })
      .catch(() => {
        if (cancelled) return;
        setLinuxDOConfigState("error");
        setGitHubConfigState("error");
        setNodeLocConfigState("error");
      });
    return () => {
      cancelled = true;
    };
  }, [t]);

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
        {error ? (
          <div role="alert" aria-live="assertive" className="mb-4 rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">
            {error}
          </div>
        ) : null}
        {linuxDOConfigState === "enabled" || linuxDOConfigState === "loading" || githubConfigState === "enabled" || githubConfigState === "loading" || nodeLocConfigState === "enabled" || nodeLocConfigState === "loading" ? (
          <div className="mb-5 space-y-4">
            {linuxDOConfigState === "enabled" || linuxDOConfigState === "loading" ? (
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
            ) : null}
            {githubConfigState === "enabled" || githubConfigState === "loading" ? (
              <button
                type="button"
                aria-busy={githubConfigState === "loading"}
                disabled={githubConfigState === "loading"}
                onClick={() => window.location.assign(githubLoginURL)}
                className="flex h-11 w-full cursor-pointer items-center justify-center gap-2 rounded-lg border border-[var(--divider)] bg-[var(--surface)] px-4 text-sm font-semibold text-[var(--ink-primary)] transition-colors duration-200 hover:bg-[var(--surface-sunken)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--brand-start)] focus-visible:ring-offset-2 disabled:cursor-wait disabled:opacity-70"
              >
                {githubConfigState === "loading" ? <Loader2 className="size-5 animate-spin" aria-hidden="true" /> : <IconGithubLogo size="large" />}
                {t(githubConfigState === "loading" ? "Loading GitHub login..." : "Continue with GitHub")}
              </button>
            ) : null}
            {nodeLocConfigState === "enabled" || nodeLocConfigState === "loading" ? (
              <button
                type="button"
                aria-busy={nodeLocConfigState === "loading"}
                disabled={nodeLocConfigState === "loading"}
                onClick={() => window.location.assign(nodeLocLoginURL)}
                className="flex h-11 w-full cursor-pointer items-center justify-center gap-2 rounded-lg border border-[var(--divider)] bg-[var(--surface)] px-4 text-sm font-semibold text-[var(--ink-primary)] transition-colors duration-200 hover:bg-[var(--surface-sunken)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--brand-start)] focus-visible:ring-offset-2 disabled:cursor-wait disabled:opacity-70"
              >
                {nodeLocConfigState === "loading" ? <Loader2 className="size-5 animate-spin" aria-hidden="true" /> : <MapPin className="size-5" aria-hidden="true" />}
                {t(nodeLocConfigState === "loading" ? "Loading NodeLoc login..." : "Continue with NodeLoc")}
              </button>
            ) : null}
            <div className="flex items-center gap-3" aria-hidden="true">
              <span className="h-px flex-1 bg-[var(--divider)]" />
              <span className="text-xs uppercase tracking-wide text-[var(--ink-muted)]">{t("or")}</span>
              <span className="h-px flex-1 bg-[var(--divider)]" />
            </div>
          </div>
        ) : null}
        {linuxDOConfigState === "error" ? (
          <div role="alert" aria-live="polite" className="mb-3 rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">{t("Could not load LinuxDO login settings. Please refresh and try again.")}</div>
        ) : null}
        {githubConfigState === "error" ? (
          <div role="alert" aria-live="polite" className="mb-5 rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">{t("Could not load GitHub login settings. Please refresh and try again.")}</div>
        ) : null}
        {nodeLocConfigState === "error" ? (
          <div role="alert" aria-live="polite" className="mb-5 rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">{t("Could not load NodeLoc login settings. Please refresh and try again.")}</div>
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

      <EmailOAuthSetupModal
        complete={completeLinuxDO}
        expiredKey="LinuxDO account setup expired. Please sign in with LinuxDO again."
        failedKey="LinuxDO account setup failed."
        getPending={getLinuxDOPending}
        onCancel={() => setEmailOAuthSetup(null)}
        onComplete={async () => {
          setEmailOAuthSetup(null);
          await refreshCurrentUser();
          void navigate({ to: consumeLoginReturnTo() as never, replace: true });
        }}
        open={emailOAuthSetup === "linuxdo"}
        preparingKey="Preparing LinuxDO account setup..."
        providerName="LinuxDO"
        sendEmailCode={sendLinuxDOEmailCode}
        titleKey="Finish LinuxDO sign-in"
        turnstileAction="linuxdo_email_code"
      />

      <EmailOAuthSetupModal
        complete={completeNodeLoc}
        expiredKey="NodeLoc account setup expired. Please sign in with NodeLoc again."
        failedKey="NodeLoc account setup failed."
        getPending={getNodeLocPending}
        onCancel={() => setEmailOAuthSetup(null)}
        onComplete={async () => {
          setEmailOAuthSetup(null);
          await refreshCurrentUser();
          void navigate({ to: consumeLoginReturnTo() as never, replace: true });
        }}
        open={emailOAuthSetup === "nodeloc"}
        preparingKey="Preparing NodeLoc account setup..."
        providerName="NodeLoc"
        sendEmailCode={sendNodeLocEmailCode}
        titleKey="Finish NodeLoc sign-in"
        turnstileAction="nodeloc_email_code"
      />

      <GitHubVerificationModal
        onCancel={() => setGitHubSetupOpen(false)}
        onComplete={async () => {
          setGitHubSetupOpen(false);
          await refreshCurrentUser();
          void navigate({ to: consumeLoginReturnTo() as never, replace: true });
        }}
        open={githubSetupOpen}
      />
    </div>
  );
}
