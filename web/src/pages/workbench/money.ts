import { formatCompactNumber, formatPoints } from "@/lib/points";

export { formatCompactNumber };

const ledgerScale = 1_000_000n;

function ledgerUnits(value: number) {
  if (!Number.isFinite(value) || value < 0) return null;
  const match = /^(\d+)\.(\d{6})$/.exec(value.toFixed(6));
  if (!match) return null;
  return BigInt(match[1]) * ledgerScale + BigInt(match[2]);
}

export function calculateDiscountedLedgerTotal(
  unitPrice: number,
  multiplier: number,
  quantity = 1,
) {
  const unitPriceUnits = ledgerUnits(unitPrice);
  const multiplierUnits = ledgerUnits(multiplier);
  if (
    unitPriceUnits === null ||
    multiplierUnits === null ||
    multiplierUnits > ledgerScale ||
    !Number.isSafeInteger(quantity) ||
    quantity < 0
  ) {
    return 0;
  }

  const scaledPrice = unitPriceUnits * multiplierUnits;
  let discountedUnitPrice = scaledPrice / ledgerScale;
  const remainder = scaledPrice % ledgerScale;
  const half = ledgerScale / 2n;
  if (
    remainder > half ||
    (remainder === half && discountedUnitPrice % 2n === 1n)
  ) {
    discountedUnitPrice += 1n;
  }
  return Number(discountedUnitPrice * BigInt(quantity)) / Number(ledgerScale);
}

export function formatMoneyExact(value: number) {
  if (!Number.isFinite(value)) return "0";
  return value.toFixed(6).replace(/\.?0+$/, "");
}

export function formatMoney(value: number) {
  if (!Number.isFinite(value)) return "0";
  return formatPoints(value);
}
