export type PointValue = string | number | null | undefined;

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

export function formatPoints(value: PointValue, unit: string) {
  const formatted = formatPointsValue(value);
  return formatted === "—" ? formatted : `${formatted} ${unit}`;
}
