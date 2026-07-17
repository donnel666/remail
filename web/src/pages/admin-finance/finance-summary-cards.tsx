import type { ReactNode } from "react";

import {
  IconCoinMoneyStroked,
  IconHistogram,
  IconMoneyExchangeStroked,
  IconSend,
} from "@douyinfe/semi-icons";
import { Avatar, Card, Skeleton } from "@douyinfe/semi-ui";
import { CircleDollarSign, Wallet } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { FinanceSummary } from "./admin-finance-api";
import { formatMoney } from "./finance-meta";

type MetricTone = "blue" | "cyan" | "green" | "orange" | "pink" | "purple";

interface MetricItem {
  avatarColor: MetricTone;
  icon: ReactNode;
  title: string;
  trendColor: string;
  trendData: number[];
  value: string;
}

interface MetricGroup {
  color: string;
  items: MetricItem[];
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

export function FinanceSummaryCards({
  loading,
  summary,
}: {
  loading: boolean;
  summary: FinanceSummary | null;
}) {
  const { t } = useTranslation();
  const trend = summary?.trend ?? [];
  const groups: MetricGroup[] = [
    {
      color: "bg-[color-mix(in_oklch,#3b82f6_12%,var(--semi-color-bg-0))]",
      title: groupTitle(<Wallet size={16} />, t("Platform finance")),
      items: [
        {
          avatarColor: "blue",
          icon: <IconMoneyExchangeStroked />,
          title: t("Recharge amount"),
          trendColor: "#3b82f6",
          trendData: trend.map((point) => point.recharge),
          value: `¥${formatMoney(summary?.rechargeAmount)}`,
        },
        {
          avatarColor: "purple",
          icon: <IconHistogram />,
          title: t("Spend amount"),
          trendColor: "#8b5cf6",
          trendData: trend.map((point) => point.spend),
          value: `¥${formatMoney(summary?.spendAmount)}`,
        },
        {
          avatarColor: "cyan",
          icon: <IconSend />,
          title: t("Withdraw amount"),
          trendColor: "#06b6d4",
          trendData: trend.map((point) => point.withdraw),
          value: `¥${formatMoney(summary?.withdrawAmount)}`,
        },
      ],
    },
    {
      color: "bg-[color-mix(in_oklch,#22a06b_12%,var(--semi-color-bg-0))]",
      title: groupTitle(<CircleDollarSign size={16} />, t("Platform earnings")),
      items: [
        {
          avatarColor: "green",
          icon: <IconCoinMoneyStroked />,
          title: t("Platform revenue"),
          trendColor: "#22a06b",
          trendData: trend.map((point) => point.platformRevenue),
          value: `¥${formatMoney(summary?.platformRevenue)}`,
        },
        {
          avatarColor: "purple",
          icon: <IconHistogram />,
          title: t("Account revenue"),
          trendColor: "#8b5cf6",
          trendData: trend.map((point) => point.accountRevenue),
          value: `¥${formatMoney(summary?.accountRevenue)}`,
        },
        {
          avatarColor: "orange",
          icon: <IconMoneyExchangeStroked />,
          title: t("Refund amount"),
          trendColor: "#f59e0b",
          trendData: trend.map((point) => point.refund),
          value: `¥${formatMoney(summary?.refundAmount)}`,
        },
      ],
    },
  ];

  return (
    <div className="mb-4">
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {groups.map((group, groupIndex) => (
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
                          loading={loading}
                          placeholder={
                            <Skeleton.Paragraph
                              rows={1}
                              style={{ height: 24, marginTop: 4, width: 82 }}
                            />
                          }
                        >
                          {item.value}
                        </Skeleton>
                      </div>
                    </div>
                  </div>
                  {item.trendData.length ? (
                    <div className="h-10 w-24 shrink-0">
                      <Sparkline color={item.trendColor} values={item.trendData} />
                    </div>
                  ) : null}
                </div>
              ))}
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}
