import { Button, Modal, Radio, RadioGroup } from "@douyinfe/semi-ui";
import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { SendCodeField } from "@/components/auth/SendCodeField";
import { getIamErrorMessage } from "@/lib/iam-errors";

export type EmailOAuthAccountMode = "existing" | "new";

export interface EmailOAuthPending {
  providerUserId: string;
  username: string;
  suggestedEmail: string;
  suggestedEmailExists: boolean;
  registrationEnabled: boolean;
  legacyAccount?: boolean;
}

interface EmailOAuthSetupModalProps {
  open: boolean;
  providerName: string;
  preparingKey: string;
  titleKey: string;
  expiredKey: string;
  failedKey: string;
  turnstileAction: string;
  getPending: () => Promise<EmailOAuthPending>;
  sendEmailCode: (payload: {
    mode: EmailOAuthAccountMode;
    email: string;
    turnstileToken: string;
  }) => Promise<number>;
  complete: (payload: {
    mode: EmailOAuthAccountMode;
    email: string;
    code: string;
  }) => Promise<unknown>;
  onCancel: () => void;
  onComplete: () => Promise<void> | void;
}

function removeOAuthSetupParam() {
  const params = new URLSearchParams(window.location.search);
  params.delete("oauth_setup");
  const search = params.toString();
  window.history.replaceState({}, "", window.location.pathname + (search ? `?${search}` : "") + window.location.hash);
}

export function EmailOAuthSetupModal({
  open,
  providerName,
  preparingKey,
  titleKey,
  expiredKey,
  failedKey,
  turnstileAction,
  getPending,
  sendEmailCode,
  complete,
  onCancel,
  onComplete,
}: EmailOAuthSetupModalProps) {
  const { t } = useTranslation();
  const [pending, setPending] = useState<EmailOAuthPending | null>(null);
  const [loading, setLoading] = useState(false);
  const [mode, setMode] = useState<EmailOAuthAccountMode>("new");
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [completing, setCompleting] = useState(false);

  useEffect(() => {
    if (!open) {
      setPending(null);
      setCode("");
      setNotice("");
      setError("");
      return;
    }
    let cancelled = false;
    setLoading(true);
    void getPending()
      .then((value) => {
        if (cancelled) return;
        const legacy = value.legacyAccount ?? false;
        const nextMode = legacy || (!value.suggestedEmailExists && value.registrationEnabled) ? "new" : "existing";
        setPending(value);
        setMode(nextMode);
        setEmail(legacy && value.suggestedEmailExists ? "" : value.suggestedEmail);
      })
      .catch((nextError) => {
        if (!cancelled) setError(getIamErrorMessage(t, nextError, expiredKey));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [expiredKey, getPending, open, t]);

  const close = () => {
    if (completing) return;
    removeOAuthSetupParam();
    onCancel();
  };

  const finish = async () => {
    if (!email.trim()) {
      setError(t("Please enter your email."));
      return;
    }
    if (!code.trim()) {
      setError(t("Please enter the verification code."));
      return;
    }
    setCompleting(true);
    setError("");
    setNotice("");
    try {
      await complete({ mode, email: email.trim(), code: code.trim() });
      removeOAuthSetupParam();
      await onComplete();
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, failedKey));
    } finally {
      setCompleting(false);
    }
  };

  const legacy = pending?.legacyAccount ?? false;
  return (
    <Modal
      centered
      footer={
        <div className="flex justify-end gap-2">
          <Button disabled={completing} onClick={close} theme="light" type="tertiary">{t("Cancel")}</Button>
          <Button disabled={loading || pending === null} loading={completing} onClick={() => void finish()} theme="solid" type="primary">{t("Continue")}</Button>
        </div>
      }
      maskClosable={false}
      onCancel={close}
      title={t(titleKey)}
      visible={open}
      width="min(480px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        {loading ? (
          <div className="flex items-center gap-2 py-6 text-sm text-[var(--semi-color-text-2)]" role="status">
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            {t(preparingKey)}
          </div>
        ) : null}
        {pending ? (
          <>
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm text-[var(--semi-color-text-1)]">
              <div className="font-medium text-[var(--semi-color-text-0)]">{pending.username}</div>
              <div className="mt-0.5 text-xs">{providerName} ID: {pending.providerUserId}</div>
            </div>
            <div>
              <div className="mb-2 text-sm font-medium text-[var(--semi-color-text-0)]">{t("Choose account ownership")}</div>
              <RadioGroup
                type="button"
                value={mode}
                onChange={(event) => {
                  setMode(event.target.value as EmailOAuthAccountMode);
                  setCode("");
                  setError("");
                  setNotice("");
                }}
              >
                <Radio disabled={legacy} value="existing">{t("Bind existing account")}</Radio>
                <Radio disabled={!pending.registrationEnabled && !legacy} value="new">
                  {t(legacy ? "Upgrade current account" : "Create new account")}
                </Radio>
              </RadioGroup>
              <p className="mt-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
                {t(legacy
                  ? "This legacy LinuxDO account already contains site data. Verify a new email to keep this account, balance, orders, and resources."
                  : mode === "existing"
                    ? "Verify the email of your existing account. Its email and password will not be changed."
                    : "Verify a receiving email to create an account. No password is set; use Forgot password later if needed.")}
              </p>
            </div>
            <label htmlFor={`${providerName.toLowerCase()}-account-email`} className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Email")}</span>
              <input
                id={`${providerName.toLowerCase()}-account-email`}
                autoComplete="email"
                className="input-antd h-11 w-full"
                disabled={completing}
                onChange={(event) => { setEmail(event.target.value); setCode(""); }}
                placeholder={t("Email")}
                required
                type="email"
                value={email}
              />
            </label>
          </>
        ) : null}
        {notice ? <div role="status" aria-live="polite" className="rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">{notice}</div> : null}
        {error ? <div role="alert" aria-live="assertive" className="rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">{error}</div> : null}
        {pending ? (
          <SendCodeField
            code={code}
            disabled={completing}
            email={email}
            onCodeChange={setCode}
            onError={setError}
            onNotice={setNotice}
            send={(payload) => sendEmailCode({ ...payload, mode })}
            turnstileAction={turnstileAction}
          />
        ) : null}
      </div>
    </Modal>
  );
}
