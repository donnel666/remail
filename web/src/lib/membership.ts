import type { UserResponse } from "./iam-api";

export type MembershipGroup = UserResponse["userGroup"];

export interface MembershipProgress {
  hasHigherGroup: boolean;
  nextGroup: MembershipGroup | null;
  percent: number | null;
  remaining: string | null;
}

const moneyScale = 1_000_000n;

function amount(value: string | number | undefined) {
  if (value == null || (typeof value === "number" && !Number.isFinite(value))) return null;
  const raw = typeof value === "number" ? value.toFixed(6) : value.trim();
  const match = /^(\d+)(?:\.(\d{1,6}))?$/.exec(raw);
  if (!match) return null;
  return BigInt(match[1]) * moneyScale + BigInt((match[2] ?? "").padEnd(6, "0"));
}

function money(value: bigint) {
  const whole = value / moneyScale;
  let fraction = (value % moneyScale).toString().padStart(6, "0").replace(/0+$/, "");
  if (fraction.length < 2) fraction = fraction.padEnd(2, "0");
  return `${whole}.${fraction}`;
}

export function normalizePriceMultiplier(value: string | number | undefined) {
  const raw = String(value ?? "").trim();
  return /^\d+(?:\.\d{1,6})?$/.test(raw) && Number(raw) <= 1 ? raw : "1";
}

export function priceMultiplier(value: string | number | undefined) {
  return Number(normalizePriceMultiplier(value));
}

export function formatPriceMultiplier(value: string | number | undefined) {
  return `${priceMultiplier(value).toFixed(6).replace(/\.?0+$/, "")}×`;
}

export function calculateMembershipProgress(
  currentGroup: MembershipGroup,
  groups: MembershipGroup[],
  totalRecharged: string | number | undefined,
): MembershipProgress | null {
  const currentThreshold = amount(currentGroup.topupThreshold);
  if (currentThreshold == null) return null;

  const higherGroups = groups.flatMap((group) => {
    const threshold = amount(group.topupThreshold);
    return group.enabled && threshold != null && threshold > currentThreshold
      ? [{ group, threshold }]
      : [];
  });
  const next = higherGroups
    .filter(({ group }) => group.autoUpgradeEnabled)
    .sort((left, right) =>
      left.threshold === right.threshold
        ? right.group.id - left.group.id
        : left.threshold < right.threshold ? -1 : 1,
    )[0] ?? null;

  if (!next) {
    return {
      hasHigherGroup: higherGroups.length > 0,
      nextGroup: null,
      percent: 100,
      remaining: "0.00",
    };
  }

  const total = amount(totalRecharged);
  if (total == null) {
    return {
      hasHigherGroup: true,
      nextGroup: next.group,
      percent: null,
      remaining: null,
    };
  }
  const range = next.threshold - currentThreshold;
  const completed = total <= currentThreshold
    ? 0n
    : total >= next.threshold ? range : total - currentThreshold;

  return {
    hasHigherGroup: true,
    nextGroup: next.group,
    percent: range > 0 ? Number((completed * 10_000n) / range) / 100 : 100,
    remaining: money(next.threshold > total ? next.threshold - total : 0n),
  };
}
