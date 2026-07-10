import { Button, Toast } from "@douyinfe/semi-ui";
import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type { FetchHandler, FetchSource } from "./types";

const defaultFetchDelaySeconds = 5;

interface SharedFetch {
  autoSubscribers: number;
  handler: FetchHandler;
  inFlight?: Promise<number | void>;
  listeners: Set<() => void>;
  nextAt: number;
}

const sharedFetches = new Map<string, SharedFetch>();
let tickerID: number | undefined;

function ensureTicker() {
  if (tickerID === undefined) {
    tickerID = window.setInterval(() => {
      const now = Date.now();
      for (const [key, entry] of sharedFetches) {
        for (const listener of entry.listeners) listener();
        if (
          entry.autoSubscribers > 0 &&
          !document.hidden &&
          now >= entry.nextAt &&
          !entry.inFlight
        ) {
          void runSharedFetch(key, "auto");
        }
      }
    }, 1000);
  }
}

function stopTickerWhenIdle() {
  if (sharedFetches.size === 0 && tickerID !== undefined) {
      window.clearInterval(tickerID);
      tickerID = undefined;
  }
}

function retryDelaySeconds(value: number | void) {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) {
    return defaultFetchDelaySeconds;
  }
  return Math.max(1, Math.ceil(value));
}

function notify(entry: SharedFetch) {
  for (const listener of entry.listeners) listener();
}

function runSharedFetch(key: string, source: FetchSource) {
  const entry = sharedFetches.get(key);
  if (!entry) return Promise.resolve();
  if (entry.inFlight) return entry.inFlight;

  const request = Promise.resolve(entry.handler(source))
    .then((delay) => {
      entry.nextAt = Date.now() + retryDelaySeconds(delay) * 1000;
      return delay;
    })
    .catch((error: unknown) => {
      entry.nextAt = Date.now() + defaultFetchDelaySeconds * 1000;
      throw error;
    })
    .finally(() => {
      entry.inFlight = undefined;
      notify(entry);
      if (entry.listeners.size === 0) {
        sharedFetches.delete(key);
        stopTickerWhenIdle();
      }
    });
  entry.inFlight = request;
  notify(entry);
  return request;
}

export function FetchControl({
  actionLabelKey = "Fetch mail",
  autoEnabled = true,
  compact = false,
  fetchKey,
  onFetch,
  variant = "default",
}: {
  actionLabelKey?: string;
  autoEnabled?: boolean;
  compact?: boolean;
  fetchKey: string;
  onFetch: FetchHandler;
  variant?: "default" | "code";
}) {
  const { t } = useTranslation();
  const [, forceRender] = useState(0);
  const isCodeVariant = variant === "code";

  useEffect(() => {
    if (!fetchKey) return;
    let entry = sharedFetches.get(fetchKey);
    if (!entry) {
      entry = {
        autoSubscribers: 0,
        handler: onFetch,
        listeners: new Set(),
        nextAt: Date.now(),
      };
      sharedFetches.set(fetchKey, entry);
    }
    entry.handler = onFetch;
    if (autoEnabled) entry.autoSubscribers += 1;
    const listener = () => forceRender((value) => value + 1);
    entry.listeners.add(listener);
    ensureTicker();
    listener();
    return () => {
      const current = sharedFetches.get(fetchKey);
      if (!current) return;
      current.listeners.delete(listener);
      if (autoEnabled) current.autoSubscribers = Math.max(0, current.autoSubscribers - 1);
      if (current.listeners.size === 0 && !current.inFlight) {
        sharedFetches.delete(fetchKey);
      }
      stopTickerWhenIdle();
    };
  }, [autoEnabled, fetchKey]);

  useEffect(() => {
    const current = sharedFetches.get(fetchKey);
    if (current) current.handler = onFetch;
  }, [fetchKey, onFetch]);

  const entry = sharedFetches.get(fetchKey);
  const fetching = Boolean(entry?.inFlight);
  const countdown = entry
    ? Math.max(0, Math.ceil((entry.nextAt - Date.now()) / 1000))
    : 0;

  const submitManual = useCallback(() => {
    void runSharedFetch(fetchKey, "manual").catch(() => {
      Toast.error(t("Fetch failed"));
    });
  }, [fetchKey, t]);

  return (
    <div className={`workbench-fetch-control ${isCodeVariant ? "is-code" : ""}`}>
      {autoEnabled ? (
        <span className="font-mono-data text-[12px] text-[var(--semi-color-text-2)]">
          {t("Auto fetch in seconds", { seconds: countdown })}
        </span>
      ) : null}
      <Button
        disabled={countdown > 0 || fetching}
        icon={<RefreshCw size={14} />}
        loading={fetching && !isCodeVariant}
        onClick={submitManual}
        size={compact ? "small" : "default"}
        theme={isCodeVariant ? "outline" : "solid"}
        type={isCodeVariant ? "tertiary" : "primary"}
      >
        {countdown > 0 && isCodeVariant
          ? `${countdown}s`
          : countdown > 0
            ? t("Fetch cooldown", { seconds: countdown })
            : t(actionLabelKey)}
      </Button>
    </div>
  );
}
