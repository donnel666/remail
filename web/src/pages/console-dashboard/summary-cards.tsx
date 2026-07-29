import type { ReactNode } from "react";

import {
  IconHistogram,
  IconMoneyExchangeStroked,
  IconPulse,
  IconStopwatchStroked,
} from "@douyinfe/semi-icons";
import { Avatar, Card, Skeleton, Tag } from "@douyinfe/semi-ui";
import { useNavigate } from "@tanstack/react-router";
import {
  Activity,
  BadgeCheck,
  Gauge,
  Wallet,
  Zap,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import type { DashboardData } from "@/lib/dashboard-api";
import { formatPoints } from "@/lib/points";
import type { APIKeyRealtimeUsageResponse } from "@/lib/openapi-credentials-api";

type MetricTone = "blue" | "cyan" | "green" | "orange" | "pink" | "purple";

interface MetricItem {
  avatarColor: MetricTone;
  icon: ReactNode;
  title: string;
  trendColor: string;
  trendData?: number[];
  value: string;
}

interface MetricGroup {
  color: string;
  items: MetricItem[];
  loading?: boolean;
  status?: string;
  title: ReactNode;
}

function Sparkline({ color, values }: { color: string; values: number[] }) {
  const safeValues = values.length > 1 ? values : [0, 0];
  const max = Math.max(...safeValues);
  const min = Math.min(...safeValues);
  const range = max - min || 1;
  const points = safeValues
    .map((value, index) => {
      const x = (index / Math.max(1, safeValues.length - 1)) * 100;
      const y = 26 - ((value - min) / range) * 20;
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");

  return (
    <svg
      aria-hidden
      className="h-10 w-24 overflow-visible"
      preserveAspectRatio="none"
      viewBox="0 0 100 32"
    >
      <polyline
        fill="none"
        points={points}
        stroke={color}
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="2"
      />
    </svg>
  );
}

function groupTitle(icon: ReactNode, text: string) {
  return (
    <div className="flex items-center gap-2">
      {icon}
      {text}
    </div>
  );
}

function percent(part: number, whole: number) {
  return whole ? (part / whole) * 100 : 0;
}

export function DashboardSummaryCards({
  data,
  loading,
  realtimeLoading,
  realtimeUnavailable,
  usage,
}: {
  data: DashboardData | null;
  loading: boolean;
  realtimeLoading: boolean;
  realtimeUnavailable: boolean;
  usage: APIKeyRealtimeUsageResponse | null;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const stats = data?.stats;
  const trend = data?.trend ?? [];
  const codeSuccessTrend = trend.map((point) =>
    percent(point.receivedCodes, point.codeOrders),
  );
  const purchaseSuccessTrend = trend.map((point) =>
    percent(point.activatedPurchases, point.purchaseOrders),
  );
  const receiptTimeTrend = trend.map((point) => point.averageCodeReceiptSeconds);
  const activationTimeTrend = trend.map(
    (point) => point.averagePurchaseActivationSeconds,
  );

  const groupedStatsData: MetricGroup[] = [
    {
      color: "bg-[color-mix(in_oklch,#3b82f6_12%,var(--semi-color-bg-0))]",
      title: groupTitle(<Wallet size={16} />, t("Account overview")),
      items: [
        {
          avatarColor: "blue",
          icon: <IconMoneyExchangeStroked />,
          title: t("Wallet balance"),
          trendColor: "#3b82f6",
          value: formatPoints(stats?.walletBalance),
        },
        {
          avatarColor: "purple",
          icon: <IconHistogram />,
          title: t("Historical spend"),
          trendColor: "#8b5cf6",
          value: formatPoints(stats?.historicalSpend),
        },
      ],
    },
    {
      color: "bg-[color-mix(in_oklch,#f59e0b_12%,var(--semi-color-bg-0))]",
      title: groupTitle(<Zap size={16} />, t("Code order fulfillment")),
      items: [
        {
          avatarColor: "green",
          icon: <IconPulse />,
          title: t("Code success rate"),
          trendColor: "#22a06b",
          trendData: codeSuccessTrend,
          value: `${stats?.codeSuccessRate ?? 0}%`,
        },
        {
          avatarColor: "purple",
          icon: <IconStopwatchStroked />,
          title: t("Average code receipt time"),
          trendColor: "#8b5cf6",
          trendData: receiptTimeTrend,
          value: `${stats?.averageCodeReceiptSeconds ?? 0}s`,
        },
      ],
    },
    {
      color: "bg-[color-mix(in_oklch,#22a06b_12%,var(--semi-color-bg-0))]",
      title: groupTitle(
        <BadgeCheck size={16} />,
        t("Purchase order fulfillment"),
      ),
      items: [
        {
          avatarColor: "green",
          icon: <IconPulse />,
          title: t("Purchase activation success rate"),
          trendColor: "#22a06b",
          trendData: purchaseSuccessTrend,
          value: `${stats?.purchaseActivationSuccessRate ?? 0}%`,
        },
        {
          avatarColor: "cyan",
          icon: <IconStopwatchStroked />,
          title: t("Average purchase activation time"),
          trendColor: "#06b6d4",
          trendData: activationTimeTrend,
          value: `${stats?.averagePurchaseActivationSeconds ?? 0}s`,
        },
      ],
    },
    {
      color: "bg-[color-mix(in_oklch,#8b5cf6_12%,var(--semi-color-bg-0))]",
      loading: realtimeLoading,
      status: realtimeUnavailable
        ? t("Real-time data is temporarily unavailable")
        : undefined,
      title: groupTitle(<Gauge size={16} />, t("Real-time load")),
      items: [
        {
          avatarColor: "pink",
          icon: <Activity size={16} />,
          title: t("Active API requests"),
          trendColor: "#ec4899",
          value: usage ? usage.activeRequests.toLocaleString() : "—",
        },
        {
          avatarColor: "purple",
          icon: <IconPulse />,
          title: t("Requests per minute (RPM)"),
          trendColor: "#8b5cf6",
          value: usage ? usage.requestsPerMinute.toLocaleString() : "—",
        },
      ],
    },
  ];

  return (
    <div className="mb-4">
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
        {groupedStatsData.map((group, groupIndex) => (
          <Card
            bodyStyle={{ padding: 10 }}
            bordered
            className={`console-dashboard-stat-card ${group.color} w-full border-0 !rounded-2xl`}
            headerLine
            key={groupIndex}
            title={group.title}
          >
            <div className="space-y-4">
              {group.items.map((item, itemIndex) => (
                <div className="flex items-center justify-between" key={itemIndex}>
                  <div className="flex min-w-0 items-center">
                    <Avatar
                      className="mr-3 shrink-0"
                      color={item.avatarColor}
                      size="small"
                    >
                      {item.icon}
                    </Avatar>
                    <div className="min-w-0">
                      <div className="truncate text-xs text-[var(--semi-color-text-2)]">
                        {item.title}
                      </div>
                      <div className="text-lg font-semibold text-[var(--semi-color-text-0)]">
                        <Skeleton
                          active
                          loading={group.loading ?? loading}
                          placeholder={
                            <Skeleton.Paragraph
                              rows={1}
                              style={{ height: 24, marginTop: 4, width: 65 }}
                            />
                          }
                        >
                          {item.value}
                        </Skeleton>
                      </div>
                    </div>
                  </div>
                  {groupIndex === 0 && itemIndex === 0 ? (
                    <Tag
                      className="cursor-pointer"
                      color="white"
                      onClick={() => void navigate({ to: "/wallet" })}
                      shape="circle"
                      size="large"
                    >
                      {t("Recharge")}
                    </Tag>
                  ) : item.trendData?.length ? (
                    <div className="h-10 w-24 shrink-0">
                      <Sparkline color={item.trendColor} values={item.trendData} />
                    </div>
                  ) : null}
                </div>
              ))}
              {group.status ? (
                <div
                  aria-live="polite"
                  className="text-xs text-[var(--semi-color-danger)]"
                  role="status"
                >
                  {group.status}
                </div>
              ) : null}
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}
