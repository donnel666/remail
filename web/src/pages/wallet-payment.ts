export function calculateRechargePaymentAmount(amountValue: unknown, rateValue: unknown, capValue: unknown) {
  const amount = Number(amountValue);
  const rate = Number(rateValue);
  const cap = Number(capValue);
  if (!Number.isFinite(amount) || amount <= 0 || !Number.isFinite(rate) || rate < 0) return "0.00";
  const fee = Math.min(Math.ceil(Number((amount * rate).toFixed(8))) / 100, Number.isFinite(cap) && cap > 0 ? cap : Number.POSITIVE_INFINITY);
  return (amount + fee).toFixed(2);
}
