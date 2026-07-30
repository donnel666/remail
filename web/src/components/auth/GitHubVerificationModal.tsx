import { Button, Modal } from "@douyinfe/semi-ui";
import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { SendCodeField } from "@/components/auth/SendCodeField";
import {
  completeGitHub,
  getGitHubPending,
  sendGitHubEmailCode,
  type GitHubPendingResponse,
} from "@/lib/iam-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

interface GitHubVerificationModalProps {
  open: boolean;
  onCancel: () => void;
  onComplete: (intent: GitHubPendingResponse["intent"]) => Promise<void> | void;
}

function removeOAuthSetupParam() {
  const params = new URLSearchParams(window.location.search);
  params.delete("oauth_setup");
  const search = params.toString();
  window.history.replaceState(
    {},
    "",
    window.location.pathname + (search ? `?${search}` : "") + window.location.hash
  );
}

export function GitHubVerificationModal({
  open,
  onCancel,
  onComplete,
}: GitHubVerificationModalProps) {
  const { t } = useTranslation();
  const [pending, setPending] = useState<GitHubPendingResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [completing, setCompleting] = useState(false);
  const [code, setCode] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");

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
    void getGitHubPending()
      .then((value) => {
        if (!cancelled) setPending(value);
      })
      .catch((nextError) => {
        if (!cancelled) {
          setError(
            getIamErrorMessage(
              t,
              nextError,
              "GitHub account verification expired. Please sign in with GitHub again."
            )
          );
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, t]);

  const close = () => {
    if (completing) return;
    removeOAuthSetupParam();
    onCancel();
  };

  const finish = async () => {
    if (!pending || !code.trim()) {
      setError(t("Please enter the verification code."));
      return;
    }
    setCompleting(true);
    setError("");
    setNotice("");
    try {
      await completeGitHub({ code: code.trim() });
      removeOAuthSetupParam();
      await onComplete(pending.intent);
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, "GitHub account verification failed."));
    } finally {
      setCompleting(false);
    }
  };

  return (
    <Modal
      centered
      footer={
        <div className="flex justify-end gap-2">
          <Button disabled={completing} onClick={close} theme="light" type="tertiary">
            {t("Cancel")}
          </Button>
          <Button
            disabled={loading || pending === null}
            loading={completing}
            onClick={() => void finish()}
            theme="solid"
            type="primary"
          >
            {t("Continue")}
          </Button>
        </div>
      }
      maskClosable={false}
      onCancel={close}
      title={t(pending?.intent === "bind" ? "Verify before binding GitHub" : "Verify your existing account")}
      visible={open}
      width="min(480px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        {loading ? (
          <div className="flex items-center gap-2 py-6 text-sm text-[var(--semi-color-text-2)]" role="status">
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            {t("Preparing GitHub account verification...")}
          </div>
        ) : null}

        {pending ? (
          <>
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm text-[var(--semi-color-text-1)]">
              <div className="font-medium text-[var(--semi-color-text-0)]">{pending.username}</div>
              <div className="mt-0.5 text-xs">GitHub ID: {pending.providerUserId}</div>
            </div>
            <p className="text-sm leading-6 text-[var(--semi-color-text-1)]">
              {t(
                pending.intent === "bind"
                  ? "Verify your current account email before binding this GitHub account."
                  : "This email already belongs to an account. Verify the current mailbox before linking GitHub and signing in."
              )}
            </p>
            <label htmlFor="github-account-email" className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Email")}
              </span>
              <input
                id="github-account-email"
                autoComplete="email"
                className="input-antd h-11 w-full"
                readOnly
                type="email"
                value={pending.email}
              />
            </label>
          </>
        ) : null}

        {notice ? (
          <div role="status" aria-live="polite" className="rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">
            {notice}
          </div>
        ) : null}
        {error ? (
          <div role="alert" aria-live="assertive" className="rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300">
            {error}
          </div>
        ) : null}

        {pending ? (
          <SendCodeField
            code={code}
            disabled={completing}
            email={pending.email}
            onCodeChange={setCode}
            onError={setError}
            onNotice={setNotice}
            send={sendGitHubEmailCode}
            turnstileAction="github_email_code"
          />
        ) : null}
      </div>
    </Modal>
  );
}
