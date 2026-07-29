export type PointValue = string | number | null | undefined;

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

export function normalizePointValue(value: PointValue) {
  const raw =
    typeof value === "number"
      ? Number.isFinite(value)
        ? value.toFixed(6)
        : ""
      : String(value ?? "").trim();
  const match = /^(-?)(\d+)(?:\.(\d{1,6}))?$/.exec(raw);
  if (!match) return "";
  const integer = match[2].replace(/^0+(?=\d)/, "");
  const fraction = (match[3] ?? "").replace(/0+$/, "");
  const zero = integer === "0" && fraction === "";
  return `${zero ? "" : match[1]}${integer}${fraction ? `.${fraction}` : ""}`;
}

export function formatPointsValue(value: PointValue) {
  const normalized = normalizePointValue(value);
  if (!normalized) return "—";
  const [integer, fraction] = normalized.split(".");
  const grouped = integer.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return fraction ? `${grouped}.${fraction}` : grouped;
}

export function formatPoints(value: PointValue) {
  const normalized = normalizePointValue(value);
  if (!normalized) return "—";
  const numeric = Number(normalized);
  if (Number.isFinite(numeric) && Math.abs(numeric) >= 1000) return formatCompactNumber(numeric);
  const formatted = formatPointsValue(value);
  return formatted;
}
