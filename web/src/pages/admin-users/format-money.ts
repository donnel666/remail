import { formatPointsValue } from "@/lib/points";

export function formatMoney(value: string | number | null | undefined) {
  return formatPointsValue(value);
}
