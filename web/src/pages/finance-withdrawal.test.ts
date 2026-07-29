import { describe, expect, it } from "vitest";

import {
  isPositiveLedgerAmount,
  sumLedgerAmounts,
  validateWithdrawal,
} from "./finance-withdrawal";

describe("supplier withdrawals", () => {
  it("requires a payment QR code only for Alipay withdrawals", () => {
    expect(
      validateWithdrawal({
        amount: "10",
        available: "20",
        destination: "alipay",
        paymentQrCode: "",
      }),
    ).toBe("Payment QR code is required.");
    expect(
      validateWithdrawal({
        amount: "10",
        available: "20",
        destination: "wallet",
        paymentQrCode: "",
      }),
    ).toBeNull();
  });

  it("rejects amounts above the supplier balance", () => {
    expect(
      validateWithdrawal({
        amount: "21",
        available: "20",
        destination: "wallet",
        paymentQrCode: "",
      }),
    ).toBe("Withdrawal exceeds withdrawable balance.");

    expect(
      validateWithdrawal({
        amount: "1000000000000.000000",
        available: "999999999999.999999",
        destination: "wallet",
        paymentQrCode: "",
      }),
    ).toBe("Withdrawal exceeds withdrawable balance.");
  });

  it("rejects fractional points for Alipay and wallet withdrawals", () => {
    expect(
      validateWithdrawal({
        amount: "12.345678",
        available: "20",
        destination: "alipay",
        paymentQrCode: "data:image/png;base64,qr",
      }),
    ).toBe("Amount must be an integer.");
    expect(
      validateWithdrawal({
        amount: "12.345678",
        available: "20",
        destination: "wallet",
        paymentQrCode: "",
      }),
    ).toBe("Amount must be an integer.");
    expect(
      validateWithdrawal({
        amount: "12.000000",
        available: "20.500000",
        destination: "wallet",
        paymentQrCode: "",
      }),
    ).toBeNull();
  });

  it("calculates ledger balances without converting them to floating point", () => {
    expect(sumLedgerAmounts("999999999999.999999", "0.000001")).toBe(
      "1000000000000.000000",
    );
    expect(isPositiveLedgerAmount("0.000001")).toBe(true);
    expect(isPositiveLedgerAmount("0.000000")).toBe(false);
  });
});
