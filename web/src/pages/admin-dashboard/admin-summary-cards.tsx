import type { ReactNode } from "react";

import {
  IconCoinMoneyStroked,
  IconHistogram,
  IconMoneyExchangeStroked,
  IconPulse,
  IconSend,
  IconStopwatchStroked,
  IconTextStroked,
} from "@douyinfe/semi-icons";
import { Avatar, Card, Skeleton } from "@douyinfe/semi-ui";
import {
  Activity,
  Database,
  Globe,
  Mail,
  MailCheck,
  UserPlus,
  UserRoundCheck,
  Users,
  Wallet,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import type { AdminDashboardData } from "./admin-dashboard-mock";

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

function formatCount(value: number | undefined) {
  return (value ?? 0).toLocaleString("zh-CN");
}

function formatMoney(value: string | number | null | undefined) {
  const parsed = Number(value ?? 0);
  if (!Number.isFinite(parsed)) return "0.00";
  return parsed.toLocaleString(undefined, {
    maximumFractionDigits: 2,
    minimumFractionDigits: 2,
  });
}

function formatRate(value: number | undefined) {
  return `${(value ?? 0).toFixed(1)}%`;
}

export function AdminDashboardSummaryCards({
  data,
  loading,
}: {
  data: AdminDashboardData | null;
  loading: boolean;
}) {
  const { t } = useTranslation();
  const stats = data?.stats;
  const trend = data?.trend ?? [];

  const groupedStatsData: MetricGroup[] = [
    {
      color: "bg-[color-mix(in_oklch,#3b82f6_12%,var(--semi-color-bg-0))]",
      title: groupTitle(<Wallet size={16} />, t("Platform finance")),
      items: [
        {
          avatarColor: "blue",
          icon: <IconMoneyExchangeStroked />,
          title: t("Recharge amount"),
          trendColor: "#3b82f6",
          trendData: trend.map((point) => point.rechargeAmount),
          value: `¥${formatMoney(stats?.rechargeAmount)}`,
        },
        {
          avatarColor: "purple",
          icon: <IconHistogram />,
          title: t("Spend amount"),
          trendColor: "#8b5cf6",
          trendData: trend.map((point) => point.spendAmount),
          value: `¥${formatMoney(stats?.spendAmount)}`,
        },
        {
          avatarColor: "green",
          icon: <IconCoinMoneyStroked />,
          title: t("Platform revenue"),
          trendColor: "#22a06b",
          trendData: trend.map((point) => point.platformRevenue),
          value: `¥${formatMoney(stats?.platformRevenue)}`,
        },
        {
          avatarColor: "orange",
          icon: <IconMoneyExchangeStroked />,
          title: t("Refund amount"),
          trendColor: "#f59e0b",
          trendData: trend.map((point) => point.refundAmount),
          value: `¥${formatMoney(stats?.refundAmount)}`,
        },
        {
          avatarColor: "cyan",
          icon: <IconCoinMoneyStroked />,
          title: t("Withdraw amount"),
          trendColor: "#06b6d4",
          trendData: trend.map((point) => point.withdrawAmount),
          value: `¥${formatMoney(stats?.withdrawAmount)}`,
        },
      ],
    },
    {
      color: "bg-[color-mix(in_oklch,#22a06b_12%,var(--semi-color-bg-0))]",
      title: groupTitle(<Activity size={16} />, t("Platform operations")),
      items: [
        {
          avatarColor: "orange",
          icon: <IconSend />,
          title: t("Total orders"),
          trendColor: "#f59e0b",
          trendData: trend.map((point) => point.orders),
          value: formatCount(stats?.totalOrders),
        },
        {
          avatarColor: "cyan",
          icon: <IconPulse />,
          title: t("Successful code receipts"),
          trendColor: "#06b6d4",
          trendData: trend.map((point) => point.successfulCodeReceipts),
          value: formatCount(stats?.successfulCodeReceipts),
        },
        {
          avatarColor: "blue",
          icon: <Users size={16} />,
          title: t("Total users"),
          trendColor: "#3b82f6",
          trendData: trend.map((point) => point.totalUsers),
          value: formatCount(stats?.totalUsers),
        },
        {
          avatarColor: "green",
          icon: <UserRoundCheck size={16} />,
          title: t("Active users"),
          trendColor: "#22a06b",
          trendData: trend.map((point) => point.activeUsers),
          value: formatCount(stats?.activeUsers),
        },
        {
          avatarColor: "purple",
          icon: <UserPlus size={16} />,
          title: t("New users"),
          trendColor: "#8b5cf6",
          trendData: trend.map((point) => point.newUsers),
          value: formatCount(stats?.newUsers),
        },
      ],
    },
    {
      color: "bg-[color-mix(in_oklch,#06b6d4_12%,var(--semi-color-bg-0))]",
      title: groupTitle(<Database size={16} />, t("Microsoft Emails")),
      items: [
        {
          avatarColor: "blue",
          icon: <Mail size={16} />,
          title: t("Total Microsoft emails"),
          trendColor: "#3b82f6",
          trendData: trend.map((point) => point.microsoftTotalEmails),
          value: formatCount(stats?.microsoftTotalEmails),
        },
        {
          avatarColor: "green",
          icon: <MailCheck size={16} />,
          title: t("Available Microsoft emails"),
          trendColor: "#22a06b",
          trendData: trend.map((point) => point.microsoftAvailableEmails),
          value: formatCount(stats?.microsoftAvailableEmails),
        },
        {
          avatarColor: "orange",
          icon: <IconTextStroked />,
          title: t("Microsoft code receipts"),
          trendColor: "#f59e0b",
          trendData: trend.map((point) => point.microsoftReceivedCodes),
          value: formatCount(stats?.microsoftCodeReceipts),
        },
        {
          avatarColor: "green",
          icon: <IconPulse />,
          title: t("Microsoft code success rate"),
          trendColor: "#22a06b",
          trendData: trend.map((point) => point.microsoftCodeSuccessRate),
          value: formatRate(stats?.microsoftCodeSuccessRate),
        },
        {
          avatarColor: "purple",
          icon: <IconStopwatchStroked />,
          title: t("Average code receipt time"),
          trendColor: "#8b5cf6",
          trendData: trend.map((point) => point.microsoftAverageCodeReceiptSeconds),
          value: `${stats?.microsoftAverageCodeReceiptSeconds ?? 0}s`,
        },
      ],
    },
    {
      color: "bg-[color-mix(in_oklch,#8b5cf6_12%,var(--semi-color-bg-0))]",
      title: groupTitle(<Globe size={16} />, t("Domain Emails")),
      items: [
        {
          avatarColor: "purple",
          icon: <Mail size={16} />,
          title: t("Total domain emails"),
          trendColor: "#8b5cf6",
          trendData: trend.map((point) => point.domainTotalMailboxes),
          value: formatCount(stats?.domainTotalMailboxes),
        },
        {
          avatarColor: "cyan",
          icon: <MailCheck size={16} />,
          title: t("Available domain emails"),
          trendColor: "#06b6d4",
          trendData: trend.map((point) => point.domainAvailableMailboxes),
          value: formatCount(stats?.domainAvailableMailboxes),
        },
        {
          avatarColor: "pink",
          icon: <IconTextStroked />,
          title: t("Domain code receipts"),
          trendColor: "#ec4899",
          trendData: trend.map((point) => point.domainReceivedCodes),
          value: formatCount(stats?.domainCodeReceipts),
        },
        {
          avatarColor: "green",
          icon: <IconPulse />,
          title: t("Domain code success rate"),
          trendColor: "#22a06b",
          trendData: trend.map((point) => point.domainCodeSuccessRate),
          value: formatRate(stats?.domainCodeSuccessRate),
        },
        {
          avatarColor: "orange",
          icon: <IconStopwatchStroked />,
          title: t("Average code receipt time"),
          trendColor: "#f59e0b",
          trendData: trend.map((point) => point.domainAverageCodeReceiptSeconds),
          value: `${stats?.domainAverageCodeReceiptSeconds ?? 0}s`,
        },
      ],
    },
  ];

  return (
    <div className="mb-4">
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
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
                          loading={loading}
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
