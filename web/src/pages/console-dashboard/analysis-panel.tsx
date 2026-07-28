import { useEffect, useMemo } from "react";

import { Card, Tabs } from "@douyinfe/semi-ui";
import { VChart, VChartCore, type ISpec } from "@visactor/react-vchart";
import { semiDesignDark, semiDesignLight } from "@visactor/vchart-semi-theme";
import { PieChart } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { DashboardData } from "@/lib/dashboard-api";

export type AnalysisView =
  | "fulfillmentTime"
  | "projects"
  | "serviceModes"
  | "spend"
  | "successRate";

const ANALYSIS_TABS: Array<{ key: AnalysisView; labelKey: string }> = [
  { key: "spend", labelKey: "Spend distribution" },
  { key: "successRate", labelKey: "Fulfillment success rate" },
  { key: "fulfillmentTime", labelKey: "Fulfillment time" },
  { key: "projects", labelKey: "Project code ranking" },
  { key: "serviceModes", labelKey: "Service mode distribution" },
];

const CHART_CONFIG = { mode: "desktop-browser" as const };
const CHART_COLORS = ["#3b82f6", "#8b5cf6", "#06b6d4", "#ec4899", "#f59e0b", "#22a06b"];

function formatMoney(value: string | number | null | undefined) {
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) return "0.00";
  return parsed.toLocaleString(undefined, {
    maximumFractionDigits: 6,
    minimumFractionDigits: 2,
  });
}

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

function DashboardChartEmpty({ loading }: { loading: boolean }) {
  const { t } = useTranslation();

  return (
    <div className="flex h-full items-center justify-center text-sm text-[var(--semi-color-text-2)]">
      {loading ? t("Loading...") : t("No overview data")}
    </div>
  );
}

function useAnalysisSpec(data: DashboardData | null, view: AnalysisView) {
  const { t } = useTranslation();

  return useMemo<ISpec>(() => {
    const projectColors = Object.fromEntries(
      (data?.projectSeries ?? []).map((project, index) => [
        project.name,
        CHART_COLORS[index % CHART_COLORS.length],
      ]),
    );
    const totalSpend = data?.trend.reduce((sum, point) => sum + point.spend, 0) ?? 0;

    if (view === "spend") {
      const values = (data?.projectSeries ?? []).flatMap((project) =>
        (data?.trend ?? []).map((point, index) => ({
          Project: project.name,
          Time: point.label,
          Usage: project.spend[index] ?? 0,
        })),
      );

      return asChartSpec({
        color: { specified: projectColors },
        data: [{ id: "spendData", values }],
        legends: { selectMode: "single", visible: true },
        seriesField: "Project",
        title: chartTitle(t("Spend distribution"), `${t("Total spend")}：¥${formatMoney(totalSpend)}`),
        type: "line",
        xField: "Time",
        yField: "Usage",
      });
    }

    if (view === "successRate") {
      const codeSuccessLabel = t("Code success rate");
      const purchaseSuccessLabel = t("Purchase activation success rate");

      return asChartSpec({
        axes: [
          {
            label: {
              formatMethod: (value: number) => `${value}%`,
            },
            orient: "left",
          },
        ],
        color: {
          specified: {
            [codeSuccessLabel]: "#f59e0b",
            [purchaseSuccessLabel]: "#22a06b",
          },
        },
        data: [
          {
            id: "fulfillmentSuccessRateData",
            values: (data?.trend ?? []).flatMap((point) => [
              {
                Metric: codeSuccessLabel,
                Rate: point.codeOrders
                  ? Number(((point.receivedCodes / point.codeOrders) * 100).toFixed(1))
                  : 0,
                Time: point.label,
              },
              {
                Metric: purchaseSuccessLabel,
                Rate: point.purchaseOrders
                  ? Number(
                      (
                        (point.activatedPurchases / point.purchaseOrders) *
                        100
                      ).toFixed(1),
                    )
                  : 0,
                Time: point.label,
              },
            ]),
          },
        ],
        legends: { selectMode: "single", visible: true },
        seriesField: "Metric",
        title: chartTitle(
          t("Fulfillment success rate"),
          `${codeSuccessLabel}：${data?.stats.codeSuccessRate ?? 0}% / ${purchaseSuccessLabel}：${data?.stats.purchaseActivationSuccessRate ?? 0}%`,
        ),
        type: "line",
        xField: "Time",
        yField: "Rate",
      });
    }

    if (view === "fulfillmentTime") {
      const codeTimeLabel = t("Average code receipt time");
      const purchaseTimeLabel = t("Average purchase activation time");

      return asChartSpec({
        color: {
          specified: {
            [codeTimeLabel]: "#8b5cf6",
            [purchaseTimeLabel]: "#06b6d4",
          },
        },
        data: [
          {
            id: "fulfillmentTimeData",
            values: (data?.trend ?? []).flatMap((point) => [
              {
                Metric: codeTimeLabel,
                Seconds: point.averageCodeReceiptSeconds,
                Time: point.label,
              },
              {
                Metric: purchaseTimeLabel,
                Seconds: point.averagePurchaseActivationSeconds,
                Time: point.label,
              },
            ]),
          },
        ],
        legends: { selectMode: "single", visible: true },
        seriesField: "Metric",
        title: chartTitle(
          t("Fulfillment time"),
          `${codeTimeLabel}：${data?.stats.averageCodeReceiptSeconds ?? 0}s / ${purchaseTimeLabel}：${data?.stats.averagePurchaseActivationSeconds ?? 0}s`,
        ),
        type: "line",
        xField: "Time",
        yField: "Seconds",
      });
    }

    if (view === "projects") {
      const values = data?.projectCodeRanking.map((item) => ({
        Count: item.count,
        Project: item.name,
      })) ?? [];

      return asChartSpec({
        data: [{ id: "projectRankData", values }],
        legends: { visible: false },
        point: { visible: true },
        title: chartTitle(t("Project code ranking"), ""),
        type: "line",
        xField: "Project",
        yField: "Count",
      });
    }

    const codeLabel = t("Code receiving");
    const purchaseLabel = t("Purchase");

    return asChartSpec({
      categoryField: "type",
      color: {
        specified: {
          [codeLabel]: "#f59e0b",
          [purchaseLabel]: "#3b82f6",
        },
      },
      data: [
        {
          id: "serviceModeData",
          values: [
            { type: codeLabel, value: data?.codeRatio ?? 0 },
            { type: purchaseLabel, value: data?.purchaseRatio ?? 0 },
          ],
        },
      ],
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
        t("Service mode distribution"),
        `${codeLabel} ${data?.codeRatio ?? 0}% / ${purchaseLabel} ${data?.purchaseRatio ?? 0}%`,
      ),
      type: "pie",
      valueField: "value",
    });
  }, [data, t, view]);
}

export function DashboardAnalysisPanel({
  data,
  loading,
  onViewChange,
  view,
}: {
  data: DashboardData | null;
  loading: boolean;
  onViewChange: (view: AnalysisView) => void;
  view: AnalysisView;
}) {
  const { t } = useTranslation();
  const spec = useAnalysisSpec(data, view);
  const hasData = view === "spend"
    ? Boolean(data?.projectSeries.length)
    : view === "projects"
      ? Boolean(data?.projectCodeRanking.length)
      : Boolean(data?.trend.length);

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
            {t("Data analysis")}
          </div>
          <Tabs activeKey={view} onChange={(key) => onViewChange(key as AnalysisView)} type="slash">
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
          <DashboardChartEmpty loading={loading} />
        )}
      </div>
    </Card>
  );
}
