export type TopupTier = { amount: number; bonus: number };

function parseJson(value: unknown, fallback: unknown) {
  try { return JSON.parse(String(value)); } catch { return fallback; }
}

export function parseTopupTiers(presetsValue: unknown, bonusValue: unknown): TopupTier[] {
  const presets = parseJson(presetsValue, []);
  const parsedBonuses = parseJson(bonusValue, {});
  const bonuses = parsedBonuses && typeof parsedBonuses === "object" && !Array.isArray(parsedBonuses) ? parsedBonuses as Record<string, unknown> : {};
  const amounts = Array.isArray(presets) ? presets.map(Number).filter((value) => Number.isFinite(value) && value > 0) : [];
  amounts.push(...Object.keys(bonuses).map(Number).filter((value) => Number.isFinite(value) && value > 0));
  return [...new Set(amounts)].map((amount) => ({ amount, bonus: Math.max(0, Number(bonuses[amount]) || 0) }));
}

export function serializeTopupTiers(tiers: TopupTier[]) {
  return {
    topup_amount_presets: JSON.stringify(tiers.map(({ amount }) => amount)),
    topup_amount_bonus: JSON.stringify(Object.fromEntries(tiers.filter(({ bonus }) => bonus > 0).map(({ amount, bonus }) => [amount, bonus]))),
  };
}
