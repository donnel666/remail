const RECHARGE_SOURCE_LABELS: Record<string, string> = {
  recharge_alipay: "Alipay",
  recharge_epusdt_usdt_tron: "USDT",
};

export function transactionTypeLabel(transactionType: string, bizType: string) {
  if (bizType === "supplier_settlement_cancel") return "Settlement refund";
  if (bizType === "supplier_settlement") return "Supplier settlement";
  return transactionType === "recharge"
    ? RECHARGE_SOURCE_LABELS[bizType] ?? transactionType
    : transactionType;
}
