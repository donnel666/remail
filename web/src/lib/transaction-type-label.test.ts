import { describe, expect, it } from "vitest";

import { transactionTypeLabel } from "./transaction-type-label";

describe("transactionTypeLabel", () => {
  it.each([
    ["recharge", "recharge_alipay", "Alipay"],
    ["recharge", "recharge_epusdt_usdt_tron", "USDT"],
    ["recharge", "recharge", "recharge"],
    ["debit", "recharge_alipay", "debit"],
  ])("maps %s/%s to %s", (transactionType, bizType, expected) => {
    expect(transactionTypeLabel(transactionType, bizType)).toBe(expected);
  });
});
