import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

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
  FinanceAnalysisPanel,
  type FinanceAnalysisView,
} from "./finance-analysis-panel";
import {
  getFinanceSummary,
  type FinanceSummary,
} from "./admin-finance-api";
import { FinanceDashboardHeader } from "./finance-dashboard-header";
import { FinanceRankingPanel } from "./finance-ranking-panel";
import { FinanceSummaryCards } from "./finance-summary-cards";

function greetingKey(hours: number) {
  if (hours < 12) return "Good morning";
  if (hours < 14) return "Good noon";
  if (hours < 18) return "Good afternoon";
  return "Good evening";
}

export function OverviewPanel({ tabsArea }: { tabsArea: ReactNode }) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const [createdAtRange, setCreatedAtRange] = useSharedDashboardDateRange();
  const [summary, setSummary] = useState<FinanceSummary | null>(null);
  const [loading, setLoading] = useState(true);
  const [analysisView, setAnalysisView] = useState<FinanceAnalysisView>("cashflow");
  const requestSequence = useRef(0);
  const displayName =
    currentUser?.nickname || currentUser?.name || t("Administrator");

  const load = useCallback(async () => {
    const requestID = ++requestSequence.current;
    setLoading(true);
    try {
      const summaryResult = await getFinanceSummary({
        createdFrom: createdFromISOString(createdAtRange),
        createdTo: createdToISOString(createdAtRange),
      });
      if (requestID !== requestSequence.current) return;
      setSummary(summaryResult);
    } catch (error) {
      if (requestID !== requestSequence.current) return;
      setSummary(null);
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
      <FinanceDashboardHeader
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

      <div className="mb-4">{tabsArea}</div>

      <FinanceSummaryCards loading={loading} summary={summary} />

      <section className="mb-4 flex flex-col gap-6">
        <FinanceRankingPanel
          items={summary?.hotProjects ?? []}
          kind="project"
          loading={loading}
          title={t("Hot projects")}
        />
        <FinanceRankingPanel
          items={summary?.hotProducts ?? []}
          kind="product"
          loading={loading}
          title={t("Hot products")}
        />
      </section>

      <FinanceAnalysisPanel
        loading={loading}
        onViewChange={setAnalysisView}
        summary={summary}
        view={analysisView}
      />
    </div>
  );
}
