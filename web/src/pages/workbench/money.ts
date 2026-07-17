const compactNumberFormatter = new Intl.NumberFormat("en-US", {
  compactDisplay: "short",
  maximumFractionDigits: 1,
  notation: "compact",
  useGrouping: false,
});

export function formatCompactNumber(value: number) {
  if (!Number.isFinite(value)) return "0";
  return compactNumberFormatter.format(value);
}

export function formatMoneyExact(value: number) {
  if (!Number.isFinite(value)) return "￥0";
  return "￥" + value.toFixed(6).replace(/\.?0+$/, "");
}

export function formatMoney(value: number) {
  if (Math.abs(value) < 1000) return formatMoneyExact(value);
  return "￥" + formatCompactNumber(value);
}
