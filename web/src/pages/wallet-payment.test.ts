import { describe, expect, it } from "vitest";

import { calculateRechargePaymentAmount } from "./wallet-payment";

describe("wallet recharge payment amount", () => {
  it("rounds a percentage fee up to the next cent and applies a cent-based cap", () => {
    expect(calculateRechargePaymentAmount(1, 0.6, 0)).toBe("1.01");
    expect(calculateRechargePaymentAmount(200, 2.5, 3)).toBe("203.00");
  });
});
