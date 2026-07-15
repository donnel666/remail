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
  AdminDashboardAnalysisPanel,
  type AdminAnalysisView,
} from "./admin-dashboard/admin-analysis-panel";
import {
  getAdminDashboardData,
  type AdminDashboardData,
} from "./admin-dashboard/admin-dashboard-mock";
import { AdminDashboardHeader } from "./admin-dashboard/admin-dashboard-header";
import { AdminRankingPanel } from "./admin-dashboard/admin-ranking-panel";
import { AdminDashboardSummaryCards } from "./admin-dashboard/admin-summary-cards";

function greetingKey(hours: number) {
  if (hours < 12) return "Good morning";
  if (hours < 14) return "Good noon";
  if (hours < 18) return "Good afternoon";
  return "Good evening";
}

export default function AdminDashboard() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [createdAtRange, setCreatedAtRange] = useSharedDashboardDateRange();
  const [data, setData] = useState<AdminDashboardData | null>(null);
  const [loading, setLoading] = useState(true);
  const [analysisView, setAnalysisView] = useState<AdminAnalysisView>("finance");
  const requestSequence = useRef(0);
  const displayName =
    currentUser?.nickname || currentUser?.name || t("Administrator");

  const load = useCallback(async () => {
    const requestID = ++requestSequence.current;
    setLoading(true);
    try {
      const result = await getAdminDashboardData({
        from: createdFromISOString(createdAtRange),
        to: createdToISOString(createdAtRange),
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

  return (
    <div className="console-dashboard-page h-full w-full px-[13px] py-5 md:px-8">
      <AdminDashboardHeader
        dateRangePresets={dateRangePresets}
        displayName={displayName}
        greeting={t(greetingKey(new Date().getHours()))}
        loading={loading}
        onDateRangeChange={(value) => {
          const next = normalizeDateRangeValue(value);
          if (next.length === 2) setCreatedAtRange(next);
        }}
        onRefresh={() => void load()}
        range={createdAtRange}
        t={t}
      />

      <AdminDashboardSummaryCards data={data} loading={loading} />

      <section className="mb-4 flex flex-col gap-6">
        <AdminRankingPanel
          items={(data?.projectInventoryRanking ?? []).map((item) => ({
            name: item.name,
            rank: item.rank,
            value: item.available,
          }))}
          kind="inventory"
          loading={loading}
          title={t("Project inventory alert ranking")}
        />
        <AdminRankingPanel
          items={(data?.projectCodeRanking ?? []).map((item) => ({
            name: item.name,
            rank: item.rank,
            value: item.count,
          }))}
          kind="project"
          loading={loading}
          title={t("Project code receipt ranking")}
        />
      </section>

      <AdminDashboardAnalysisPanel
        data={data}
        loading={loading}
        onViewChange={setAnalysisView}
        view={analysisView}
      />
    </div>
  );
}
