import { Button, Toast } from "@douyinfe/semi-ui";
import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type { FetchHandler, FetchSource } from "./types";

const defaultFetchDelaySeconds = 5;

interface SharedFetch {
  autoSubscribers: number;
  handlers: Map<symbol, SharedFetchHandler>;
  inFlight?: Promise<number | void>;
  lastSource?: FetchSource;
  listeners: Set<() => void>;
  nextAt: number;
}

interface SharedFetchHandler {
  autoEnabled: boolean;
  handler: FetchHandler;
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
          void runSharedFetch(key, "auto").catch(() => undefined);
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

function resolveHandler(
  entry: SharedFetch,
  source: FetchSource,
  subscriberID?: symbol,
): FetchHandler | undefined {
  if (subscriberID) {
    return entry.handlers.get(subscriberID)?.handler;
  }

  let handler: FetchHandler | undefined;
  for (const subscriber of entry.handlers.values()) {
    if (source === "auto" && !subscriber.autoEnabled) continue;
    handler = subscriber.handler;
  }
  return handler;
}

function runSharedFetch(
  key: string,
  source: FetchSource,
  subscriberID?: symbol,
) {
  const entry = sharedFetches.get(key);
  if (!entry) return Promise.resolve();
  if (entry.inFlight) return entry.inFlight;
  const handler = resolveHandler(entry, source, subscriberID);
  if (!handler) return Promise.resolve();
  entry.lastSource = source;

  const request = Promise.resolve()
    .then(() => handler(source))
    .then((delay) => {
      entry.nextAt =
        source === "auto" && entry.autoSubscribers === 0
          ? Date.now()
          : Date.now() + retryDelaySeconds(delay) * 1000;
      return delay;
    })
    .catch((error: unknown) => {
      entry.nextAt =
        source === "auto" && entry.autoSubscribers === 0
          ? Date.now()
          : Date.now() + defaultFetchDelaySeconds * 1000;
      throw error;
    })
    .finally(() => {
      entry.inFlight = undefined;
      notify(entry);
      if (entry.listeners.size === 0) {
        if (sharedFetches.get(key) === entry) sharedFetches.delete(key);
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
  const [subscriberID] = useState(() => Symbol("fetch-control"));
  const isCodeVariant = variant === "code";

  useEffect(() => {
    if (!fetchKey) return;
    let entry = sharedFetches.get(fetchKey);
    if (!entry) {
      entry = {
        autoSubscribers: 0,
        handlers: new Map(),
        listeners: new Set(),
        nextAt: Date.now(),
      };
      sharedFetches.set(fetchKey, entry);
    }
    entry.handlers.set(subscriberID, { autoEnabled, handler: onFetch });
    if (autoEnabled) entry.autoSubscribers += 1;
    const listener = () => forceRender((value) => value + 1);
    entry.listeners.add(listener);
    ensureTicker();
    listener();
    return () => {
      const current = sharedFetches.get(fetchKey);
      if (!current) return;
      current.listeners.delete(listener);
      current.handlers.delete(subscriberID);
      if (autoEnabled) {
        current.autoSubscribers = Math.max(0, current.autoSubscribers - 1);
        if (
          current.autoSubscribers === 0 &&
          current.lastSource === "auto"
        ) {
          current.nextAt = Date.now();
          notify(current);
        }
      }
      if (current.listeners.size === 0 && !current.inFlight) {
        sharedFetches.delete(fetchKey);
      }
      stopTickerWhenIdle();
    };
  }, [autoEnabled, fetchKey, subscriberID]);

  useEffect(() => {
    const current = sharedFetches.get(fetchKey);
    const subscriber = current?.handlers.get(subscriberID);
    if (subscriber) subscriber.handler = onFetch;
  }, [fetchKey, onFetch, subscriberID]);

  const entry = sharedFetches.get(fetchKey);
  const fetching = Boolean(entry?.inFlight);
  const countdown = entry
    ? Math.max(0, Math.ceil((entry.nextAt - Date.now()) / 1000))
    : 0;

  const submitManual = useCallback(() => {
    void runSharedFetch(fetchKey, "manual", subscriberID).catch(() => {
      Toast.error(t("Fetch failed"));
    });
  }, [fetchKey, subscriberID, t]);

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
