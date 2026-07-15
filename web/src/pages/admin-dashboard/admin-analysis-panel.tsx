import { useEffect, useMemo } from "react";

import { Card, Tabs } from "@douyinfe/semi-ui";
import { VChart, VChartCore, type ISpec } from "@visactor/react-vchart";
import { semiDesignDark, semiDesignLight } from "@visactor/vchart-semi-theme";
import { PieChart } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { AdminDashboardData } from "./admin-dashboard-mock";

export type AdminAnalysisView =
  | "codes"
  | "finance"
  | "inventory"
  | "orders"
  | "successRate"
  | "users";

const ANALYSIS_TABS: Array<{ key: AdminAnalysisView; labelKey: string }> = [
  { key: "finance", labelKey: "Finance trend" },
  { key: "orders", labelKey: "Order trend" },
  { key: "users", labelKey: "User growth" },
  { key: "codes", labelKey: "Code receipt trend" },
  { key: "successRate", labelKey: "Code success rate" },
  { key: "inventory", labelKey: "Inventory trend" },
];

const CHART_CONFIG = { mode: "desktop-browser" as const };

function formatMoney(value: string | number | null | undefined) {
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) return "0.00";
  return parsed.toLocaleString(undefined, {
    maximumFractionDigits: 2,
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

function useAnalysisSpec(data: AdminDashboardData | null, view: AdminAnalysisView) {
  const { t } = useTranslation();

  return useMemo<ISpec>(() => {
    if (view === "finance") {
      const labels = {
        recharge: t("Recharge amount"),
        refund: t("Refund amount"),
        revenue: t("Platform revenue"),
        spend: t("Spend amount"),
        withdraw: t("Withdraw amount"),
      };
      const values = (data?.trend ?? []).flatMap((point) => [
        { Amount: point.rechargeAmount, Metric: labels.recharge, Time: point.label },
        { Amount: point.spendAmount, Metric: labels.spend, Time: point.label },
        { Amount: point.platformRevenue, Metric: labels.revenue, Time: point.label },
        { Amount: point.refundAmount, Metric: labels.refund, Time: point.label },
        { Amount: point.withdrawAmount, Metric: labels.withdraw, Time: point.label },
      ]);

      return asChartSpec({
        color: {
          specified: {
            [labels.recharge]: "#3b82f6",
            [labels.spend]: "#8b5cf6",
            [labels.revenue]: "#22a06b",
            [labels.refund]: "#f59e0b",
            [labels.withdraw]: "#06b6d4",
          },
        },
        data: [{ id: "adminFinanceTrendData", values }],
        legends: { selectMode: "single", visible: true },
        seriesField: "Metric",
        title: chartTitle(
          t("Finance trend"),
          `${labels.recharge}：¥${formatMoney(data?.stats.rechargeAmount)} / ${labels.spend}：¥${formatMoney(data?.stats.spendAmount)}`,
        ),
        type: "line",
        xField: "Time",
        yField: "Amount",
      });
    }

    if (view === "orders") {
      const orderLabel = t("Total orders");
      const successLabel = t("Successful code receipts");
      const values = (data?.trend ?? []).flatMap((point) => [
        { Count: point.orders, Metric: orderLabel, Time: point.label },
        { Count: point.successfulCodeReceipts, Metric: successLabel, Time: point.label },
      ]);

      return asChartSpec({
        color: { specified: { [orderLabel]: "#06b6d4", [successLabel]: "#22a06b" } },
        data: [{ id: "adminOrderTrendData", values }],
        legends: { selectMode: "single", visible: true },
        seriesField: "Metric",
        title: chartTitle(
          t("Order trend"),
          `${orderLabel}：${(data?.stats.totalOrders ?? 0).toLocaleString("zh-CN")}`,
        ),
        type: "line",
        xField: "Time",
        yField: "Count",
      });
    }

    if (view === "users") {
      const totalLabel = t("Total users");
      const activeLabel = t("Active users");
      const newLabel = t("New users");
      const values = (data?.trend ?? []).flatMap((point) => [
        { Count: point.totalUsers, Metric: totalLabel, Time: point.label },
        { Count: point.activeUsers, Metric: activeLabel, Time: point.label },
        { Count: point.newUsers, Metric: newLabel, Time: point.label },
      ]);

      return asChartSpec({
        color: {
          specified: {
            [activeLabel]: "#22a06b",
            [newLabel]: "#8b5cf6",
            [totalLabel]: "#3b82f6",
          },
        },
        data: [{ id: "adminUserGrowthData", values }],
        legends: { selectMode: "single", visible: true },
        seriesField: "Metric",
        title: chartTitle(
          t("User growth"),
          `${totalLabel}：${(data?.stats.totalUsers ?? 0).toLocaleString("zh-CN")} / ${newLabel}：${(data?.stats.newUsers ?? 0).toLocaleString("zh-CN")}`,
        ),
        type: "line",
        xField: "Time",
        yField: "Count",
      });
    }

    if (view === "codes") {
      const microsoftLabel = t("Microsoft code receipts");
      const domainLabel = t("Domain code receipts");
      const values = (data?.trend ?? []).flatMap((point) => [
        { Count: point.microsoftReceivedCodes, Metric: microsoftLabel, Time: point.label },
        { Count: point.domainReceivedCodes, Metric: domainLabel, Time: point.label },
      ]);

      return asChartSpec({
        color: {
          specified: { [domainLabel]: "#8b5cf6", [microsoftLabel]: "#3b82f6" },
        },
        data: [{ id: "adminCodeTrendData", values }],
        legends: { selectMode: "single", visible: true },
        seriesField: "Metric",
        title: chartTitle(
          t("Code receipt trend"),
          `${t("Total code receipts")}：${(data?.stats.successfulCodeReceipts ?? 0).toLocaleString("zh-CN")}`,
        ),
        type: "line",
        xField: "Time",
        yField: "Count",
      });
    }

    if (view === "successRate") {
      const microsoftLabel = t("Microsoft code success rate");
      const domainLabel = t("Domain code success rate");
      const values = (data?.trend ?? []).flatMap((point) => [
        { Metric: microsoftLabel, Rate: point.microsoftCodeSuccessRate, Time: point.label },
        { Metric: domainLabel, Rate: point.domainCodeSuccessRate, Time: point.label },
      ]);

      return asChartSpec({
        axes: [
          {
            label: { formatMethod: (value: number) => `${value}%` },
            orient: "left",
          },
        ],
        color: {
          specified: { [domainLabel]: "#ec4899", [microsoftLabel]: "#22a06b" },
        },
        data: [{ id: "adminSuccessRateData", values }],
        legends: { selectMode: "single", visible: true },
        seriesField: "Metric",
        title: chartTitle(
          t("Code success rate"),
          `${microsoftLabel}：${(data?.stats.microsoftCodeSuccessRate ?? 0).toFixed(1)}% / ${domainLabel}：${(data?.stats.domainCodeSuccessRate ?? 0).toFixed(1)}%`,
        ),
        type: "line",
        xField: "Time",
        yField: "Rate",
      });
    }

    const labels = {
      domainAvailable: t("Available domain emails"),
      domainTotal: t("Total domain emails"),
      microsoftAvailable: t("Available Microsoft emails"),
      microsoftTotal: t("Total Microsoft emails"),
    };
    const values = (data?.trend ?? []).flatMap((point) => [
      { Count: point.microsoftTotalEmails, Metric: labels.microsoftTotal, Time: point.label },
      {
        Count: point.microsoftAvailableEmails,
        Metric: labels.microsoftAvailable,
        Time: point.label,
      },
      { Count: point.domainTotalMailboxes, Metric: labels.domainTotal, Time: point.label },
      {
        Count: point.domainAvailableMailboxes,
        Metric: labels.domainAvailable,
        Time: point.label,
      },
    ]);

    return asChartSpec({
      color: {
        specified: {
          [labels.microsoftTotal]: "#3b82f6",
          [labels.microsoftAvailable]: "#06b6d4",
          [labels.domainTotal]: "#8b5cf6",
          [labels.domainAvailable]: "#ec4899",
        },
      },
      data: [{ id: "adminInventoryTrendData", values }],
      legends: { selectMode: "single", visible: true },
      seriesField: "Metric",
      title: chartTitle(
        t("Inventory trend"),
        `${labels.microsoftAvailable}：${(data?.stats.microsoftAvailableEmails ?? 0).toLocaleString("zh-CN")} / ${labels.domainAvailable}：${(data?.stats.domainAvailableMailboxes ?? 0).toLocaleString("zh-CN")}`,
      ),
      type: "line",
      xField: "Time",
      yField: "Count",
    });
  }, [data, t, view]);
}

export function AdminDashboardAnalysisPanel({
  data,
  loading,
  onViewChange,
  view,
}: {
  data: AdminDashboardData | null;
  loading: boolean;
  onViewChange: (view: AdminAnalysisView) => void;
  view: AdminAnalysisView;
}) {
  const { t } = useTranslation();
  const spec = useAnalysisSpec(data, view);
  const hasData = Boolean(data?.trend.length);

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
            {t("Platform data analysis")}
          </div>
          <Tabs
            activeKey={view}
            onChange={(key) => onViewChange(key as AdminAnalysisView)}
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
          <DashboardChartEmpty loading={loading} />
        )}
      </div>
    </Card>
  );
}
