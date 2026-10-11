import { describe, expect, it } from "vitest";

import { transactionTypeLabel } from "./transaction-type-label";

describe("transactionTypeLabel", () => {
  it.each([
    ["recharge", "recharge_alipay", "Alipay"],
    ["recharge", "recharge_epusdt_usdt_tron", "USDT"],
    ["recharge", "recharge", "recharge"],
    ["debit", "recharge_alipay", "debit"],
    ["credit", "supplier_settlement", "Supplier settlement"],
    ["manual_adjustment", "supplier_settlement_cancel", "Settlement refund"],
  ])("maps %s/%s to %s", (transactionType, bizType, expected) => {
    expect(transactionTypeLabel(transactionType, bizType)).toBe(expected);
  });
});
