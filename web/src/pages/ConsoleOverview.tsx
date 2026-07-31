import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { useAuth } from "@/context/auth-provider";
import { useSharedDashboardDateRange } from "@/hooks/use-shared-dashboard-date-range";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
} from "@/pages/resources/date-range-filter";

import {
  getDashboardData,
  type DashboardData,
} from "@/lib/dashboard-api";
import {
  getAPIKeyRealtimeUsage,
  type APIKeyRealtimeUsageResponse,
} from "@/lib/openapi-credentials-api";
import { subscribeWalletUpdated } from "@/lib/wallet-events";

import {
  DashboardAnalysisPanel,
  type AnalysisView,
} from "./console-dashboard/analysis-panel";
import { DashboardHeader } from "./console-dashboard/dashboard-header";
import { RankingPanel } from "./console-dashboard/ranking-panel";
import { DashboardSummaryCards } from "./console-dashboard/summary-cards";

function greetingKey(hours: number) {
  if (hours < 12) return "Good morning";
  if (hours < 14) return "Good noon";
  if (hours < 18) return "Good afternoon";
  return "Good evening";
}

export default function ConsoleOverview() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [createdAtRange, setCreatedAtRange] = useSharedDashboardDateRange();
  const [data, setData] = useState<DashboardData | null>(null);
  const [usage, setUsage] = useState<APIKeyRealtimeUsageResponse | null>(null);
  const [realtimeLoading, setRealtimeLoading] = useState(true);
  const [realtimeUnavailable, setRealtimeUnavailable] = useState(false);
  const [loading, setLoading] = useState(true);
  const [analysisView, setAnalysisView] = useState<AnalysisView>("spend");
  const requestSequence = useRef(0);
  const realtimeGeneration = useRef(0);
  const realtimeInFlight = useRef<Promise<void> | null>(null);
  const displayName = currentUser?.nickname || currentUser?.name || t("User");

  const load = useCallback(async () => {
    const requestID = ++requestSequence.current;
    setLoading(true);
    try {
      const result = await getDashboardData({
        createdFrom: createdFromISOString(createdAtRange),
        createdTo: createdToISOString(createdAtRange),
      });
      if (requestID !== requestSequence.current) return;
      setData(result);
    } catch (error) {
      if (requestID !== requestSequence.current) return;
      setData(null);
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      if (requestID === requestSequence.current) setLoading(false);
    }
  }, [createdAtRange, t]);

  useEffect(() => {
    void load();
    const unsubscribe = subscribeWalletUpdated(() => void load());
    return () => {
      unsubscribe();
      requestSequence.current += 1;
    };
  }, [load]);

  const refreshRealtimeUsage = useCallback(async () => {
    if (realtimeInFlight.current) {
      await realtimeInFlight.current;
      return;
    }
    const generation = realtimeGeneration.current;
    const request = (async () => {
      try {
        const result = await getAPIKeyRealtimeUsage();
        if (generation !== realtimeGeneration.current) return;
        setUsage(result);
        setRealtimeUnavailable(false);
      } catch {
        if (generation !== realtimeGeneration.current) return;
        setUsage(null);
        setRealtimeUnavailable(true);
      } finally {
        if (generation === realtimeGeneration.current) setRealtimeLoading(false);
      }
    })();
    realtimeInFlight.current = request;
    try {
      await request;
    } finally {
      if (realtimeInFlight.current === request) realtimeInFlight.current = null;
    }
  }, []);

  useEffect(() => {
    let stopped = false;
    let polling = false;
    let timer: number | undefined;
    const schedule = () => {
      if (!stopped && !document.hidden) {
        timer = window.setTimeout(() => void poll(), 10_000);
      }
    };
    const poll = async () => {
      if (stopped || polling || document.hidden) return;
      polling = true;
      await refreshRealtimeUsage();
      polling = false;
      schedule();
    };
    const handleVisibilityChange = () => {
      if (timer !== undefined) window.clearTimeout(timer);
      timer = undefined;
      if (!document.hidden) void poll();
    };

    document.addEventListener("visibilitychange", handleVisibilityChange);
    void poll();
    return () => {
      stopped = true;
      if (timer !== undefined) window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      realtimeGeneration.current += 1;
      realtimeInFlight.current = null;
    };
  }, [refreshRealtimeUsage]);

  const greeting = t(greetingKey(new Date().getHours()));

  return (
    <div className="console-content-width console-dashboard-page h-full py-5">
      <DashboardHeader
        dateRangePresets={dateRangePresets}
        displayName={displayName}
        greeting={greeting}
        loading={loading}
        onDateRangeChange={(value) => {
          const next = normalizeDateRangeValue(value);
          if (next.length === 2) setCreatedAtRange(next);
        }}
        onRefresh={() => {
          void load();
          void refreshRealtimeUsage();
        }}
        range={createdAtRange}
        t={t}
      />

      <DashboardSummaryCards
        data={data}
        loading={loading}
        realtimeLoading={realtimeLoading}
        realtimeUnavailable={realtimeUnavailable}
        usage={usage}
      />

      <section className="mb-4 flex flex-col gap-6">
        <RankingPanel
          currentUserRank={data?.todayCurrentUserRank}
          items={data?.todayCodeRanking ?? []}
          kind="today"
          loading={loading}
          title={t("Today successful order ranking")}
        />
        <RankingPanel
          currentUserRank={data?.historicalCurrentUserRank}
          items={data?.historicalCodeRanking ?? []}
          kind="history"
          loading={loading}
          title={t("All-time successful order ranking")}
        />
      </section>

      <DashboardAnalysisPanel
        data={data}
        loading={loading}
        onViewChange={setAnalysisView}
        view={analysisView}
      />
    </div>
  );
}
