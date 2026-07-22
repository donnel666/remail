import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { TurnstileField } from "@/components/auth/TurnstileField";
import { IamApiError } from "@/lib/iam-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { validateRegistrationEmail } from "@/lib/registration-email";

const RESEND_COOLDOWN_SECONDS = 60;

interface SendCodePayload {
  email: string;
  turnstileToken: string;
}

interface SendCodeFieldProps {
  email: string;
  code: string;
  onCodeChange: (value: string) => void;
  send: (payload: SendCodePayload) => Promise<unknown>;
  turnstileAction: string;
  disabled?: boolean;
  onNotice: (message: string) => void;
  onError: (message: string) => void;
}

/**
 * Turnstile + verification-code input + send button, with a resend countdown.
 * The countdown starts on a successful send, and is seeded from the server's
 * Retry-After when a request is throttled (HTTP 429), so the button reflects
 * the real backend cooldown instead of letting repeat clicks fail silently.
 */
export function SendCodeField({
  email,
  code,
  onCodeChange,
  send,
  turnstileAction,
  disabled,
  onNotice,
  onError,
}: SendCodeFieldProps) {
  const { t } = useTranslation();
  const [turnstileToken, setTurnstileToken] = useState("");
  const [turnstileResetKey, setTurnstileResetKey] = useState(0);
  const [requesting, setRequesting] = useState(false);
  const [cooldown, setCooldown] = useState(0);

  useEffect(() => {
    if (cooldown <= 0) return;
    const timer = window.setInterval(() => {
      setCooldown((seconds) => (seconds <= 1 ? 0 : seconds - 1));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [cooldown]);

  const handleRequestCode = async () => {
    // Clear any prior notice/error up front, so a failed resend never leaves the
    // previous send's stale "sent" success message on screen.
    onNotice("");
    onError("");

    if (!email.trim()) {
      onError(t("Please enter your email."));
      return;
    }
    if (turnstileAction === "register_email_code") {
      const emailError = validateRegistrationEmail(email);
      if (emailError) {
        onError(t(emailError));
        return;
      }
    }
    if (!turnstileToken) {
      onError(t("Please complete human verification."));
      return;
    }

    setRequesting(true);

    try {
      await send({
        email: email.trim(),
        turnstileToken,
      });
      onNotice(t("Verification code sent."));
      setCooldown(RESEND_COOLDOWN_SECONDS);
    } catch (nextError) {
      onError(getIamErrorMessage(t, nextError, "Failed to send verification code."));
      if (nextError instanceof IamApiError && nextError.retryAfterSeconds) {
        setCooldown(nextError.retryAfterSeconds);
      }
    } finally {
      setTurnstileToken("");
      setTurnstileResetKey((key) => key + 1);
      setRequesting(false);
    }
  };

  return (
    <>
      <TurnstileField
        action={turnstileAction}
        resetKey={turnstileResetKey}
        onTokenChange={setTurnstileToken}
      />
      <div className="grid grid-cols-[1fr_112px] gap-2">
        <input
          type="text"
          value={code}
          onChange={(event) => onCodeChange(event.target.value)}
          placeholder={t("Verification code")}
          className="input-antd min-w-0 w-full"
          autoComplete="one-time-code"
          required
        />
        <button
          type="button"
          onClick={() => void handleRequestCode()}
          disabled={disabled || requesting || !turnstileToken || cooldown > 0}
          className="flex h-9 w-28 items-center justify-center rounded-lg border border-[var(--divider)] px-3 text-sm font-medium text-[var(--ink-secondary)] transition-colors hover:bg-[var(--surface-hover)] disabled:cursor-not-allowed disabled:opacity-70"
        >
          {requesting ? (
            <Loader2 className="size-4 animate-spin" />
          ) : cooldown > 0 ? (
            `${cooldown}s`
          ) : (
            t("Send code")
          )}
        </button>
      </div>
    </>
  );
}
