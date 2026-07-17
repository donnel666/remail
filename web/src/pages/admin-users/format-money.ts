// Canonical money formatter for the admin-users surface. Kept in a pure module
// (no UI imports) so it stays unit-testable without pulling semi-ui into the
// vitest runtime; user-meta.tsx re-exports it, so callers import it unchanged.
export function formatMoney(value: string | number | null | undefined) {
  if (value == null) return "0.00";
  // Format the exact decimal string directly — no Number() round-trip — so
  // backend amounts like "0.000001" never pick up float artifacts. Computed
  // numbers are rounded to ledger precision (6dp) first, then reused.
  const raw =
    typeof value === "number"
      ? Number.isFinite(value)
        ? value.toFixed(6)
        : ""
      : value.trim();
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(raw);
  if (!match) return "0.00";
  const [, sign, intPart, fraction = ""] = match;
  let frac = fraction.slice(0, 6).replace(/0+$/, "");
  if (frac.length < 2) frac = frac.padEnd(2, "0");
  const grouped = intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return `${sign}${grouped}.${frac}`;
}
