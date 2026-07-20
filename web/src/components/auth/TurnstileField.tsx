import { Loader2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { getTurnstileConfig } from "@/lib/iam-api";

const SCRIPT_SRC =
  "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";

interface TurnstileAPI {
  render(
    container: HTMLElement,
    options: {
      sitekey: string;
      action: string;
      theme: "auto";
      size: "flexible";
      callback: (token: string) => void;
      "expired-callback": () => void;
      "error-callback": () => void;
    }
  ): string;
  remove(widgetId: string): void;
}

declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}

let scriptPromise: Promise<TurnstileAPI> | undefined;
let siteKeyPromise: Promise<string> | undefined;

function loadScript() {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  if (scriptPromise) return scriptPromise;

  scriptPromise = new Promise<TurnstileAPI>((resolve, reject) => {
    const script = document.createElement("script");
    script.src = SCRIPT_SRC;
    script.async = true;
    script.defer = true;
    script.onload = () => {
      if (window.turnstile) {
        resolve(window.turnstile);
        return;
      }
      script.remove();
      scriptPromise = undefined;
      reject(new Error("Turnstile did not initialize"));
    };
    script.onerror = () => {
      script.remove();
      scriptPromise = undefined;
      reject(new Error("Turnstile failed to load"));
    };
    document.head.appendChild(script);
  });
  return scriptPromise;
}

function loadSiteKey() {
  siteKeyPromise ??= getTurnstileConfig()
    .then(({ siteKey }) => siteKey)
    .catch((error) => {
      siteKeyPromise = undefined;
      throw error;
    });
  return siteKeyPromise;
}

interface TurnstileFieldProps {
  action: string;
  resetKey: number;
  onTokenChange: (token: string) => void;
}

export function TurnstileField({
  action,
  resetKey,
  onTokenChange,
}: TurnstileFieldProps) {
  const { t } = useTranslation();
  const containerRef = useRef<HTMLDivElement>(null);
  const [retryKey, setRetryKey] = useState(0);
  const [failed, setFailed] = useState(false);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let active = true;
    let api: TurnstileAPI | undefined;
    let widgetId: string | undefined;
    setFailed(false);
    setLoading(true);
    onTokenChange("");

    void Promise.all([loadScript(), loadSiteKey()])
      .then(([nextAPI, siteKey]) => {
        if (!active || !containerRef.current) return;
        api = nextAPI;
        widgetId = api.render(containerRef.current, {
          sitekey: siteKey,
          action,
          theme: "auto",
          size: "flexible",
          callback: (token) => active && onTokenChange(token),
          "expired-callback": () => active && onTokenChange(""),
          "error-callback": () => {
            if (!active) return;
            onTokenChange("");
            setFailed(true);
          },
        });
        setLoading(false);
      })
      .catch(() => {
        if (!active) return;
        setLoading(false);
        setFailed(true);
      });

    return () => {
      active = false;
      if (api && widgetId) api.remove(widgetId);
    };
  }, [action, onTokenChange, resetKey, retryKey]);

  return (
    <div className="relative flex min-h-16 w-full items-center justify-center overflow-hidden">
      {failed ? (
        <button
          type="button"
          className="text-sm font-medium text-brand hover:text-brand-hover"
          onClick={() => setRetryKey((key) => key + 1)}
        >
          {t("Human verification is unavailable. Retry.")}
        </button>
      ) : (
        <>
          <div ref={containerRef} className="w-full" />
          {loading ? (
            <Loader2
              className="absolute size-4 animate-spin text-[var(--ink-muted)]"
              aria-label={t("Loading human verification")}
            />
          ) : null}
        </>
      )}
    </div>
  );
}
