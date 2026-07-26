import { Button, Card, Tag, Typography } from "@douyinfe/semi-ui";
import { BadgePercent, Crown, Gauge, RefreshCw, TrendingUp } from "lucide-react";
import type { TFunction } from "i18next";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { getUserGroups } from "@/lib/iam-api";
import {
  calculateMembershipProgress,
  type MembershipGroup,
} from "@/lib/membership";
import { formatMoney } from "@/pages/admin-users/format-money";

const { Text } = Typography;

function formatCurrency(value: string | number | undefined) {
  if (value == null || String(value).trim() === "" || !Number.isFinite(Number(value))) {
    return "-";
  }
  return `￥${formatMoney(value)}`;
}

function discountText(group: MembershipGroup, t: TFunction) {
  const ratio = Number(group.priceDiscountRatio);
  if (!Number.isFinite(ratio) || ratio < 0 || ratio >= 1) return t("Standard member pricing");
  const discount = ((1 - ratio) * 100).toFixed(4).replace(/\.?0+$/, "");
  return t("{{discount}}% off member prices", { discount });
}

export function MembershipBenefits({ group }: { group?: MembershipGroup }) {
  const { t } = useTranslation();
  if (!group) return null;

  const benefits = [
    {
      icon: <BadgePercent size={18} />,
      label: t("Member price"),
      value: discountText(group, t),
    },
    {
      icon: <Gauge size={18} />,
      label: t("API concurrency"),
      value:
        group.apiConcurrencyLimit > 0
          ? t("{{count}} concurrent requests per API key", {
              count: group.apiConcurrencyLimit,
            })
          : t("No additional group concurrency cap"),
    },
  ];

  return (
    <div className="grid gap-3 sm:grid-cols-2">
      {benefits.map((benefit) => (
        <div className="flex min-w-0 items-start gap-3" key={benefit.label}>
          <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]">
            {benefit.icon}
          </span>
          <div className="min-w-0">
            <div className="text-xs text-[var(--semi-color-text-2)]">{benefit.label}</div>
            <div className="mt-0.5 text-sm font-medium text-[var(--semi-color-text-0)]">
              {benefit.value}
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

export function MembershipOverview({
  currentGroup,
  loading,
  onRetry,
  totalRecharged,
}: {
  currentGroup?: MembershipGroup;
  loading: boolean;
  onRetry?: () => Promise<unknown> | void;
  totalRecharged?: string;
}) {
  const { t } = useTranslation();
  const [groups, setGroups] = useState<MembershipGroup[]>([]);
  const [groupsLoading, setGroupsLoading] = useState(true);
  const [groupsError, setGroupsError] = useState(false);

  const loadGroups = useCallback(async () => {
    setGroupsLoading(true);
    setGroupsError(false);
    try {
      setGroups((await getUserGroups()).groups);
    } catch {
      setGroupsError(true);
    } finally {
      setGroupsLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadGroups();
  }, [loadGroups]);

  const retry = useCallback(async () => {
    await Promise.allSettled([
      loadGroups(),
      Promise.resolve().then(() => onRetry?.()),
    ]);
  }, [loadGroups, onRetry]);

  const progress = useMemo(
    () =>
      currentGroup
        ? calculateMembershipProgress(currentGroup, groups, totalRecharged)
        : null,
    [currentGroup, groups, totalRecharged],
  );
  const currentName = currentGroup?.name || currentGroup?.code || "-";

  return (
    <Card bodyStyle={{ padding: 0 }} className="!rounded-2xl overflow-hidden border-0 shadow-sm">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--semi-color-border)] px-4 py-3">
        <div className="flex min-w-0 items-center gap-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]">
            <Crown size={18} />
          </span>
          <div className="min-w-0">
            <Text className="text-lg font-medium">{t("Membership")}</Text>
            <div className="text-xs text-[var(--semi-color-text-2)]">
              {t("Membership level and active benefits")}
            </div>
          </div>
        </div>
        <Tag color="orange" size="large">
          {t("Current tier")}: {currentName}
        </Tag>
      </div>

      <div className="grid gap-5 p-4 lg:grid-cols-[minmax(0,1.1fr)_minmax(320px,0.9fr)] lg:p-5">
        <div className="min-w-0">
          <div className="text-xs font-medium text-[var(--semi-color-text-2)]">{t("Current benefits")}</div>
          <div className="mt-1 text-xl font-semibold text-[var(--semi-color-text-0)]">{currentName}</div>
          {currentGroup?.description ? (
            <p className="mt-1 text-sm leading-6 text-[var(--semi-color-text-2)]">
              {currentGroup.description}
            </p>
          ) : null}
          <div className="mt-4">
            <MembershipBenefits group={currentGroup} />
          </div>
        </div>

        <div className="min-w-0 border-t border-[var(--semi-color-border)] pt-4 lg:border-l lg:border-t-0 lg:pl-5 lg:pt-0">
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="text-xs text-[var(--semi-color-text-2)]">{t("Cumulative recharge")}</div>
              <div className="mt-1 font-mono text-xl font-semibold tabular-nums">
                {loading ? "..." : formatCurrency(totalRecharged)}
              </div>
            </div>
            <TrendingUp className="mt-1 shrink-0 text-[var(--semi-color-primary)]" size={20} />
          </div>

          {groupsLoading || loading ? (
            <div className="mt-5 min-h-12 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-3 text-sm text-[var(--semi-color-text-1)]" role="status">
              {t("Loading...")}
            </div>
          ) : groupsError || groups.length === 0 || !progress ? (
            <div className="mt-5 flex min-h-12 flex-wrap items-center justify-between gap-2 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm text-[var(--semi-color-text-1)]" role="alert">
              <span>{t("Membership tier information is unavailable")}</span>
              <Button
                disabled={loading || groupsLoading}
                icon={<RefreshCw size={14} />}
                loading={loading || groupsLoading}
                onClick={() => void retry()}
                size="small"
                theme="borderless"
                type="primary"
              >
                {t("Try again")}
              </Button>
            </div>
          ) : progress.nextGroup && progress.percent != null && progress.remaining != null ? (
            <>
              <div className="mt-5 flex items-center justify-between gap-3 text-xs">
                <span className="text-[var(--semi-color-text-2)]">{t("Next tier")}</span>
                <span className="font-medium">{progress.nextGroup.name || progress.nextGroup.code}</span>
              </div>
              <div
                aria-label={t("Membership upgrade progress")}
                aria-valuemax={100}
                aria-valuemin={0}
                aria-valuenow={Math.round(progress.percent)}
                className="mt-2 h-2 overflow-hidden rounded-full bg-[var(--semi-color-fill-1)]"
                role="progressbar"
              >
                <div
                  className="h-full rounded-full bg-[var(--semi-color-primary)]"
                  style={{ width: `${progress.percent}%` }}
                />
              </div>
              <div className="mt-2 text-sm text-[var(--semi-color-text-1)]">
                {progress.remaining !== "0.00"
                  ? t("Recharge {{amount}} more to reach {{tier}}", {
                      amount: formatCurrency(progress.remaining),
                      tier: progress.nextGroup.name || progress.nextGroup.code,
                    })
                  : t("The {{tier}} threshold has been reached", {
                      tier: progress.nextGroup.name || progress.nextGroup.code,
                    })}
              </div>
            </>
          ) : progress.nextGroup ? (
            <div className="mt-5 flex min-h-12 flex-wrap items-center justify-between gap-2 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm text-[var(--semi-color-text-1)]" role="alert">
              <span>{t("Membership tier information is unavailable")}</span>
              <Button
                disabled={loading}
                icon={<RefreshCw size={14} />}
                loading={loading}
                onClick={() => void retry()}
                size="small"
                theme="borderless"
                type="primary"
              >
                {t("Try again")}
              </Button>
            </div>
          ) : (
            <div className="mt-5 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-3 text-sm text-[var(--semi-color-text-1)]">
              {progress.hasHigherGroup
                ? t("Higher membership tiers exist, but none are available for automatic upgrade")
                : t("You have reached the highest membership tier")}
            </div>
          )}
          <p className="mt-3 text-xs leading-5 text-[var(--semi-color-text-2)]">
            {t("Progress uses credited recharges; active benefits follow your current group")}
          </p>
        </div>
      </div>
    </Card>
  );
}
