import { useNavigate } from "@tanstack/react-router";
import { KeyRound, Loader2, ShieldCheck } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-provider";
import { LOGIN_NOTICE_KEY } from "@/lib/auth-flow";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { changePassword } from "@/lib/iam-api";

export default function Account() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { currentUser, logout } = useAuth();
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (newPassword !== confirmPassword) {
      setError(t("Passwords do not match."));
      return;
    }

    setSubmitting(true);
    setError("");

    try {
      await changePassword({ oldPassword, newPassword });
      sessionStorage.setItem(LOGIN_NOTICE_KEY, "Password changed. Please log in again.");
      await logout();
      void navigate({ to: "/login", replace: true });
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, "Password change failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="mx-auto w-full max-w-5xl px-4 py-8">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold tracking-tight text-[var(--ink-primary)]">
          {t("Account")}
        </h1>
        <p className="mt-1 text-sm text-[var(--ink-muted)]">
          {t("Manage your profile and password.")}
        </p>
      </div>

      <div className="grid gap-6 lg:grid-cols-[1fr_1.2fr]">
        <section className="rounded-xl border border-[var(--divider)] bg-[var(--surface)] p-5 shadow-sm">
          <div className="mb-4 flex items-center gap-3">
            <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-brand-subtle text-brand">
              <ShieldCheck className="size-5" />
            </span>
            <div>
              <h2 className="text-base font-semibold text-[var(--ink-primary)]">
                {t("Profile")}
              </h2>
              <p className="text-sm text-[var(--ink-muted)]">{t("Current session user")}</p>
            </div>
          </div>

          <dl className="space-y-3 text-sm">
            <div className="flex items-center justify-between gap-4">
              <dt className="text-[var(--ink-muted)]">{t("Email")}</dt>
              <dd className="truncate font-medium text-[var(--ink-primary)]">
                {currentUser?.email}
              </dd>
            </div>
            <div className="flex items-center justify-between gap-4">
              <dt className="text-[var(--ink-muted)]">{t("Nickname")}</dt>
              <dd className="truncate font-medium text-[var(--ink-primary)]">
                {currentUser?.nickname || currentUser?.name}
              </dd>
            </div>
            <div className="flex items-center justify-between gap-4">
              <dt className="text-[var(--ink-muted)]">{t("Role")}</dt>
              <dd className="font-medium text-[var(--ink-primary)]">{currentUser?.role}</dd>
            </div>
            <div className="flex items-center justify-between gap-4">
              <dt className="text-[var(--ink-muted)]">{t("Role level")}</dt>
              <dd className="font-mono-data font-medium text-[var(--ink-primary)]">
                {currentUser?.roleLevel}
              </dd>
            </div>
          </dl>
        </section>

        <section className="rounded-xl border border-[var(--divider)] bg-[var(--surface)] p-5 shadow-sm">
          <div className="mb-4 flex items-center gap-3">
            <span className="flex h-9 w-9 items-center justify-center rounded-lg bg-brand-subtle text-brand">
              <KeyRound className="size-5" />
            </span>
            <div>
              <h2 className="text-base font-semibold text-[var(--ink-primary)]">
                {t("Change password")}
              </h2>
              <p className="text-sm text-[var(--ink-muted)]">
                {t("All sessions will be invalidated after password change.")}
              </p>
            </div>
          </div>

          {error ? (
            <div className="mb-4 rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">
              {error}
            </div>
          ) : null}

          <form className="space-y-4" onSubmit={handleSubmit}>
            <input
              type="password"
              value={oldPassword}
              onChange={(event) => setOldPassword(event.target.value)}
              placeholder={t("Current password")}
              className="input-antd w-full"
              autoComplete="current-password"
              required
            />
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
              className="flex h-10 w-full items-center justify-center rounded-lg bg-gradient-to-br from-[var(--brand-start)] to-[var(--brand-end)] text-sm font-semibold text-white shadow-sm transition-all hover:shadow-md disabled:cursor-not-allowed disabled:opacity-70"
              disabled={submitting}
            >
              {submitting ? <Loader2 className="mr-2 size-4 animate-spin" /> : null}
              {t("Save password")}
            </button>
          </form>
        </section>
      </div>
    </div>
  );
}
