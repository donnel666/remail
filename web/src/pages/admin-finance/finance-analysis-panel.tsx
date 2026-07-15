import { useEffect, useMemo } from "react";

import { Card, Tabs } from "@douyinfe/semi-ui";
import { VChart, VChartCore, type ISpec } from "@visactor/react-vchart";
import { semiDesignDark, semiDesignLight } from "@visactor/vchart-semi-theme";
import { PieChart } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { FinanceSummary } from "./admin-finance-mock";
import { formatMoney } from "./finance-meta";

export type FinanceAnalysisView = "cashflow" | "structure";

const ANALYSIS_TABS: Array<{ key: FinanceAnalysisView; labelKey: string }> = [
  { key: "cashflow", labelKey: "Cashflow trend" },
  { key: "structure", labelKey: "Revenue structure" },
];

const CHART_CONFIG = { mode: "desktop-browser" as const };

function chartTitle(text: string, subtext: string) {
  return { subtext, text, visible: true };
}

function asChartSpec(spec: unknown): ISpec {
  return spec as ISpec;
}

function applyVChartSemiTheme() {
  const dark =
    document.documentElement.classList.contains("dark") ||
    document.body.getAttribute("theme-mode") === "dark";
  const theme = dark ? semiDesignDark : semiDesignLight;
  const themeName = theme.name ?? (dark ? "semiDesignDark" : "semiDesignLight");

  if (!VChartCore.ThemeManager.themeExist(themeName)) {
    VChartCore.ThemeManager.registerTheme(themeName, theme);
  }
  VChartCore.ThemeManager.setCurrentTheme(themeName);
}

function FinanceChartEmpty({ loading }: { loading: boolean }) {
  const { t } = useTranslation();

  return (
    <div className="flex h-full items-center justify-center text-sm text-[var(--semi-color-text-2)]">
      {loading ? t("Loading...") : t("No overview data")}
    </div>
  );
}

function useFinanceSpec(summary: FinanceSummary | null, view: FinanceAnalysisView) {
  const { t } = useTranslation();

  return useMemo<ISpec>(() => {
    if (view === "cashflow") {
      const labels = {
        accountRevenue: t("Account revenue"),
        platformRevenue: t("Platform revenue"),
        recharge: t("Recharge amount"),
        refund: t("Refund amount"),
        spend: t("Spend amount"),
        withdraw: t("Withdraw amount"),
      };
      const values = (summary?.trend ?? []).flatMap((point) => [
        { Amount: point.recharge, Metric: labels.recharge, Time: point.label },
        { Amount: point.spend, Metric: labels.spend, Time: point.label },
        { Amount: point.withdraw, Metric: labels.withdraw, Time: point.label },
        { Amount: point.refund, Metric: labels.refund, Time: point.label },
        {
          Amount: point.platformRevenue,
          Metric: labels.platformRevenue,
          Time: point.label,
        },
        {
          Amount: point.accountRevenue,
          Metric: labels.accountRevenue,
          Time: point.label,
        },
      ]);

      return asChartSpec({
        color: {
          specified: {
            [labels.recharge]: "#3b82f6",
            [labels.spend]: "#8b5cf6",
            [labels.withdraw]: "#06b6d4",
            [labels.refund]: "#f59e0b",
            [labels.platformRevenue]: "#22a06b",
            [labels.accountRevenue]: "#ec4899",
          },
        },
        data: [{ id: "financeCashflowData", values }],
        legends: { selectMode: "single", visible: true },
        seriesField: "Metric",
        title: chartTitle(
          t("Cashflow trend"),
          `${labels.recharge}：¥${formatMoney(summary?.rechargeAmount)} / ${labels.spend}：¥${formatMoney(summary?.spendAmount)}`,
        ),
        type: "line",
        xField: "Time",
        yField: "Amount",
      });
    }

    const values = [
      { Type: t("Platform revenue"), Value: Number(summary?.platformRevenue ?? 0) },
      { Type: t("Account revenue"), Value: Number(summary?.accountRevenue ?? 0) },
      { Type: t("Spend amount"), Value: Number(summary?.spendAmount ?? 0) },
      { Type: t("Withdraw amount"), Value: Number(summary?.withdrawAmount ?? 0) },
      { Type: t("Refund amount"), Value: Number(summary?.refundAmount ?? 0) },
    ].filter((item) => item.Value > 0);

    return asChartSpec({
      categoryField: "Type",
      color: {
        specified: {
          [t("Platform revenue")]: "#22a06b",
          [t("Account revenue")]: "#ec4899",
          [t("Spend amount")]: "#8b5cf6",
          [t("Withdraw amount")]: "#06b6d4",
          [t("Refund amount")]: "#f59e0b",
        },
      },
      data: [{ id: "financeRevenueStructureData", values }],
      innerRadius: 0.5,
      label: { visible: true },
      legends: { orient: "left", visible: true },
      outerRadius: 0.8,
      padAngle: 0.6,
      pie: {
        state: {
          hover: { lineWidth: 1, outerRadius: 0.85, stroke: "#000" },
          selected: { lineWidth: 1, outerRadius: 0.85, stroke: "#000" },
        },
        style: { cornerRadius: 10 },
      },
      title: chartTitle(
        t("Revenue structure"),
        `${t("Platform revenue")}：¥${formatMoney(summary?.platformRevenue)}`,
      ),
      type: "pie",
      valueField: "Value",
    });
  }, [summary, t, view]);
}

export function FinanceAnalysisPanel({
  loading,
  onViewChange,
  summary,
  view,
}: {
  loading: boolean;
  onViewChange: (view: FinanceAnalysisView) => void;
  summary: FinanceSummary | null;
  view: FinanceAnalysisView;
}) {
  const { t } = useTranslation();
  const spec = useFinanceSpec(summary, view);
  const hasData = Boolean(summary?.trend.length);

  useEffect(() => {
    applyVChartSemiTheme();
    const observer = new MutationObserver(applyVChartSemiTheme);
    observer.observe(document.documentElement, {
      attributeFilter: ["class"],
      attributes: true,
    });
    observer.observe(document.body, {
      attributeFilter: ["theme-mode"],
      attributes: true,
    });
    return () => observer.disconnect();
  }, []);

  return (
    <Card
      bodyStyle={{ padding: 0 }}
      bordered
      className="console-dashboard-analysis-card !rounded-2xl"
      headerLine
      title={
        <div className="flex w-full flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div className="flex items-center gap-2">
            <PieChart size={16} />
            {t("Finance data analysis")}
          </div>
          <Tabs
            activeKey={view}
            onChange={(key) => onViewChange(key as FinanceAnalysisView)}
            type="slash"
          >
            {ANALYSIS_TABS.map((tab) => (
              <Tabs.TabPane
                itemKey={tab.key}
                key={tab.key}
                tab={<span>{t(tab.labelKey)}</span>}
              />
            ))}
          </Tabs>
        </div>
      }
    >
      <div className="h-96 p-2">
        {hasData ? (
          <VChart key={view} options={CHART_CONFIG} spec={spec} />
        ) : (
          <FinanceChartEmpty loading={loading} />
        )}
      </div>
    </Card>
  );
}
