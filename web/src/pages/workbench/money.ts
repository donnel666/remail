import { formatCompactNumber, formatPoints } from "@/lib/points";

export { formatCompactNumber };

export function formatMoneyExact(value: number) {
  if (!Number.isFinite(value)) return "0";
  return value.toFixed(6).replace(/\.?0+$/, "");
}

export function formatMoney(value: number) {
  if (!Number.isFinite(value)) return "0";
  return formatPoints(value);
}
