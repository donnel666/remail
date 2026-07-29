import { Button, Card } from "@douyinfe/semi-ui";
import { Crown, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { getUserGroups } from "@/lib/iam-api";
import {
  calculateMembershipProgress,
  formatPriceMultiplier,
  type MembershipGroup,
} from "@/lib/membership";
import { formatPointsValue } from "@/lib/points";

export function MembershipOverview({
  currentGroup,
  loading = false,
  onRetry,
  totalRecharged,
}: {
  currentGroup?: MembershipGroup;
  loading?: boolean;
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
  const currentConcurrency = currentGroup
    ? currentGroup.apiConcurrencyLimit > 0
      ? currentGroup.apiConcurrencyLimit.toLocaleString()
      : t("Uses API key or system limit")
    : "-";
  const progressUnavailable =
    groupsError ||
    groups.length === 0 ||
    !progress ||
    Boolean(
      progress.nextGroup &&
        (progress.percent == null || progress.remaining == null),
    );

  return (
    <>
      <Card
        bodyStyle={{ padding: 0 }}
        className="!rounded-2xl overflow-hidden border-0 shadow-sm"
      >
        <div className="px-4 py-3">
          <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
            <div className="flex min-w-0 items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]">
                <Crown size={16} />
              </span>
              <span className="text-sm text-[var(--semi-color-text-2)]">
                {t("Group")}
              </span>
              <strong className="truncate text-sm text-[var(--semi-color-text-0)]">
                {currentName}
              </strong>
            </div>
            <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-sm text-[var(--semi-color-text-1)]">
              <span>
                {t("Multiplier")}{" "}
                <strong className="font-mono text-[var(--semi-color-text-0)]">
                  {currentGroup
                    ? formatPriceMultiplier(currentGroup.priceDiscountRatio)
                    : "-"}
                </strong>
              </span>
              <span>
                {t("Maximum concurrency")}: {" "}
                <strong className="font-mono text-[var(--semi-color-text-0)]">
                  {currentConcurrency}
                </strong>
              </span>
            </div>
          </div>

          <div aria-live="polite">
            {loading || groupsLoading ? (
              <div
                className="mt-2 text-xs text-[var(--semi-color-text-2)]"
                role="status"
              >
                {t("Loading...")}
              </div>
            ) : progressUnavailable ? (
              <div
                className="mt-2 flex flex-wrap items-center justify-between gap-2 text-xs text-[var(--semi-color-text-2)]"
                role="alert"
              >
                <span>{t("Membership tier information is unavailable")}</span>
                <Button
                  disabled={loading || groupsLoading}
                  icon={<RefreshCw size={13} />}
                  loading={loading || groupsLoading}
                  onClick={() => void retry()}
                  size="small"
                  theme="borderless"
                  type="primary"
                >
                  {t("Try again")}
                </Button>
              </div>
            ) : progress?.nextGroup &&
              progress.percent != null &&
              progress.remaining != null ? (
              <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <span className="text-xs text-[var(--semi-color-text-1)]">
                  {progress.remaining === "0.00"
                    ? t("The {{tier}} threshold has been reached", {
                        tier:
                          progress.nextGroup.name || progress.nextGroup.code,
                      })
                    : t(
                        "Recharge {{amount}} more to unlock {{tier}} benefits",
                        {
                          amount: formatPointsValue(progress.remaining),
                          tier:
                            progress.nextGroup.name || progress.nextGroup.code,
                        },
                      )}
                </span>
                <div
                  aria-label={t("Membership upgrade progress")}
                  aria-valuemax={100}
                  aria-valuemin={0}
                  aria-valuenow={Math.round(progress.percent)}
                  className="h-1.5 min-w-32 flex-1 overflow-hidden rounded-full bg-[var(--semi-color-fill-1)]"
                  role="progressbar"
                >
                  <div
                    className="h-full rounded-full bg-[var(--semi-color-primary)]"
                    style={{ width: `${progress.percent}%` }}
                  />
                </div>
                <span className="w-10 text-right font-mono text-xs text-[var(--semi-color-text-2)]">
                  {Math.round(progress.percent)}%
                </span>
              </div>
            ) : progress && !progress.nextGroup ? (
              <div className="mt-2 flex items-center gap-3 text-xs text-[var(--semi-color-text-2)]">
                <div
                  aria-label={t("Membership upgrade progress")}
                  aria-valuemax={100}
                  aria-valuemin={0}
                  aria-valuenow={100}
                  className="h-1.5 flex-1 overflow-hidden rounded-full bg-[var(--semi-color-fill-1)]"
                  role="progressbar"
                >
                  <div className="h-full w-full rounded-full bg-[var(--semi-color-primary)]" />
                </div>
                <span>
                  {progress.hasHigherGroup
                    ? t(
                        "Higher membership tiers exist, but none are available for automatic upgrade",
                      )
                    : t("You have reached the highest membership tier")}
                </span>
              </div>
            ) : null}
          </div>
        </div>
      </Card>

      <Card
        bodyStyle={{ padding: 0 }}
        className="!rounded-2xl order-last mt-5 overflow-hidden border-0 shadow-sm"
      >
        <div className="overflow-x-auto">
          <table
            aria-label={t("Membership groups")}
            className="w-full min-w-[480px] border-collapse text-left text-sm"
          >
            <thead className="bg-[var(--semi-color-fill-0)] text-xs text-[var(--semi-color-text-2)]">
              <tr>
                <th className="px-4 py-2 font-medium" scope="col">
                  {t("Group")}
                </th>
                <th className="px-4 py-2 font-medium" scope="col">
                  {t("Multiplier")}
                </th>
                <th className="px-4 py-2 font-medium" scope="col">
                  {t("Maximum concurrency")}
                </th>
              </tr>
            </thead>
            <tbody>
              {groupsLoading ? (
                <tr>
                  <td
                    className="px-4 py-4 text-center text-[var(--semi-color-text-2)]"
                    colSpan={3}
                    role="status"
                  >
                    {t("Loading...")}
                  </td>
                </tr>
              ) : groupsError ? (
                <tr>
                  <td className="px-4 py-3" colSpan={3}>
                    <div
                      className="flex flex-wrap items-center justify-between gap-2 text-sm text-[var(--semi-color-text-1)]"
                      role="alert"
                    >
                      <span>
                        {t("Membership tier information is unavailable")}
                      </span>
                      <Button
                        icon={<RefreshCw size={14} />}
                        onClick={() => void loadGroups()}
                        size="small"
                        theme="borderless"
                        type="primary"
                      >
                        {t("Try again")}
                      </Button>
                    </div>
                  </td>
                </tr>
              ) : groups.length === 0 ? (
                <tr>
                  <td
                    className="px-4 py-4 text-center text-[var(--semi-color-text-2)]"
                    colSpan={3}
                  >
                    {t("Membership tier information is unavailable")}
                  </td>
                </tr>
              ) : (
                groups.map((group) => (
                  <tr
                    className="border-t border-[var(--semi-color-border)] first:border-t-0"
                    key={group.id}
                  >
                    <td className="px-4 py-2.5 font-medium text-[var(--semi-color-text-0)]">
                      {group.name || group.code}
                    </td>
                    <td className="px-4 py-2.5 font-mono text-[var(--semi-color-text-1)]">
                      {formatPriceMultiplier(group.priceDiscountRatio)}
                    </td>
                    <td className="px-4 py-2.5 font-mono text-[var(--semi-color-text-1)]">
                      {group.apiConcurrencyLimit > 0
                        ? group.apiConcurrencyLimit.toLocaleString()
                        : t("Uses API key or system limit")}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </Card>
    </>
  );
}
