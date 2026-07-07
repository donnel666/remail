import { Button, Toast } from "@douyinfe/semi-ui";
import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import type { FetchSource } from "./types";

export function FetchControl({
  actionLabelKey = "Fetch mail",
  compact = false,
  onFetch,
  variant = "default",
}: {
  actionLabelKey?: string;
  compact?: boolean;
  onFetch: (source: FetchSource) => void | Promise<void>;
  variant?: "default" | "code";
}) {
  const { t } = useTranslation();
  const [autoCountdown, setAutoCountdown] = useState(30);
  const [manualCooldown, setManualCooldown] = useState(0);
  const isCodeVariant = variant === "code";
  const onFetchRef = useRef(onFetch);

  useEffect(() => {
    onFetchRef.current = onFetch;
  }, [onFetch]);

  const submitFetch = useCallback((source: FetchSource) => {
    try {
      const result = onFetchRef.current(source);
      if (source === "manual") {
        void Promise.resolve(result)
          .then(() => Toast.success(t("Fetch submitted")))
          .catch(() => Toast.error(t("Fetch failed")));
      }
    } catch {
      if (source === "manual") Toast.error(t("Fetch failed"));
    }
  }, [t]);

  useEffect(() => {
    const timer = window.setInterval(() => {
      setAutoCountdown((current) => {
        if (current <= 1) {
          submitFetch("auto");
          return 30;
        }
        return current - 1;
      });
      setManualCooldown((current) => Math.max(0, current - 1));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [submitFetch]);

  return (
    <div className={`workbench-fetch-control ${isCodeVariant ? "is-code" : ""}`}>
      <span className="font-mono-data text-[12px] text-[var(--semi-color-text-2)]">
        {t("Auto fetch in seconds", { seconds: autoCountdown })}
      </span>
      <Button
        disabled={manualCooldown > 0}
        icon={<RefreshCw size={14} />}
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
