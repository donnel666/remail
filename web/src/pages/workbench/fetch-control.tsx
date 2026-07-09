import { Button, Toast } from "@douyinfe/semi-ui";
import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { FetchSource } from "./types";

export function FetchControl({
  actionLabelKey = "Fetch mail",
  autoEnabled = true,
  compact = false,
  onFetch,
  variant = "default",
}: {
  actionLabelKey?: string;
  autoEnabled?: boolean;
  compact?: boolean;
  onFetch: (source: FetchSource) => void | Promise<void>;
  variant?: "default" | "code";
}) {
  const { t } = useTranslation();
  const [autoCountdown, setAutoCountdown] = useState(30);
  const [manualCooldown, setManualCooldown] = useState(0);
  const [fetching, setFetching] = useState(false);
  const isCodeVariant = variant === "code";
  const onFetchRef = useRef(onFetch);
  const inFlightRef = useRef(false);

  useEffect(() => {
    onFetchRef.current = onFetch;
  }, [onFetch]);

  const submitFetch = useCallback((source: FetchSource) => {
    if (inFlightRef.current) return;
    inFlightRef.current = true;
    setFetching(true);
    try {
      const result = onFetchRef.current(source);
      void Promise.resolve(result)
        .then(() => {
          if (source === "manual") Toast.success(t("Fetch submitted"));
        })
        .catch(() => {
          if (source === "manual") Toast.error(t("Fetch failed"));
        })
        .finally(() => {
          inFlightRef.current = false;
          setFetching(false);
        });
    } catch {
      if (source === "manual") Toast.error(t("Fetch failed"));
      inFlightRef.current = false;
      setFetching(false);
    }
  }, [t]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      setAutoCountdown((current) => {
        if (current <= 1) {
          if (autoEnabled) {
            submitFetch("auto");
          }
          return 30;
        }
        return current - 1;
      });
      setManualCooldown((current) => Math.max(0, current - 1));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [autoEnabled, submitFetch]);

  return (
    <div className={`workbench-fetch-control ${isCodeVariant ? "is-code" : ""}`}>
      {autoEnabled ? (
        <span className="font-mono-data text-[12px] text-[var(--semi-color-text-2)]">
          {t("Auto fetch in seconds", { seconds: autoCountdown })}
        </span>
      ) : null}
      <Button
        disabled={manualCooldown > 0 || fetching}
        icon={<RefreshCw size={14} />}
        loading={fetching && !isCodeVariant}
        onClick={() => {
          setManualCooldown(10);
          setAutoCountdown(30);
          submitFetch("manual");
        }}
        size={compact ? "small" : "default"}
        theme={isCodeVariant ? "outline" : "solid"}
        type={isCodeVariant ? "tertiary" : "primary"}
      >
        {manualCooldown > 0 && isCodeVariant
          ? `${manualCooldown}s`
          : manualCooldown > 0
            ? t("Fetch cooldown", { seconds: manualCooldown })
            : t(actionLabelKey)}
      </Button>
    </div>
  );
}
