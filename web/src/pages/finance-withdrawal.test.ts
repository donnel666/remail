import { describe, expect, it } from "vitest";

import {
  buildAlipayWithdrawalTicketInput,
  isPositiveLedgerAmount,
  sumLedgerAmounts,
  validateWithdrawal,
} from "./finance-withdrawal";

describe("supplier withdrawal tickets", () => {
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
        amount: "20.000001",
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

  it("keeps six-decimal point precision for Alipay and wallet withdrawals", () => {
    expect(
      validateWithdrawal({
        amount: "12.345678",
        available: "20",
        destination: "alipay",
        paymentQrCode: "data:image/png;base64,qr",
      }),
    ).toBeNull();
    expect(
      validateWithdrawal({
        amount: "12.345678",
        available: "20",
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

  it("builds a general ticket and attaches the Alipay QR code", () => {
    expect(
      buildAlipayWithdrawalTicketInput({
        amount: " 12.50 ",
        note: " 请处理 ",
        paymentQrCode: "data:image/png;base64,qr",
      }),
    ).toEqual({
      ticketType: "general",
      title: "供应商提现申请",
      firstMessage: "提现积分：12.50 积分\n提现方式：支付宝\n备注：请处理",
      attachments: ["data:image/png;base64,qr"],
    });
  });
});
