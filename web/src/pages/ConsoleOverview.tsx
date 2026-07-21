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
  const [loading, setLoading] = useState(true);
  const [analysisView, setAnalysisView] = useState<AnalysisView>("spend");
  const requestSequence = useRef(0);
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
    return () => {
      requestSequence.current += 1;
    };
  }, [load]);

  const greeting = t(greetingKey(new Date().getHours()));

  return (
    <div className="console-dashboard-page h-full w-full px-[13px] py-5 md:px-8">
      <DashboardHeader
        dateRangePresets={dateRangePresets}
        displayName={displayName}
        greeting={greeting}
        loading={loading}
        onDateRangeChange={(value) => {
          const next = normalizeDateRangeValue(value);
          if (next.length === 2) setCreatedAtRange(next);
        }}
        onRefresh={() => void load()}
        range={createdAtRange}
        t={t}
      />

      <DashboardSummaryCards data={data} loading={loading} />

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
