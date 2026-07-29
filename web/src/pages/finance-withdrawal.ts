import type { CreateTicketInput } from "./tickets/tickets-api";

export type WithdrawalDestination = "alipay" | "wallet";

const LEDGER_SCALE = 1_000_000n;

function ledgerUnits(value: string) {
  const match = /^(\d+)(?:\.(\d{1,6}))?$/.exec(value.trim());
  if (!match) return null;
  return BigInt(match[1]) * LEDGER_SCALE + BigInt((match[2] ?? "").padEnd(6, "0"));
}

export function sumLedgerAmounts(left: string, right: string) {
  const leftUnits = ledgerUnits(left);
  const rightUnits = ledgerUnits(right);
  if (leftUnits === null || rightUnits === null) return null;
  const total = leftUnits + rightUnits;
  return `${total / LEDGER_SCALE}.${String(total % LEDGER_SCALE).padStart(6, "0")}`;
}

export function isPositiveLedgerAmount(value: string) {
  return (ledgerUnits(value) ?? 0n) > 0n;
}

export function validateWithdrawal(input: {
  amount: string;
  available: string;
  destination: WithdrawalDestination;
  paymentQrCode: string;
}) {
  const amount = input.amount.trim();
  const amountUnits = ledgerUnits(amount);
  if (amountUnits === null || amountUnits <= 0n) {
    return "Amount must be positive.";
  }
  if (amountUnits > (ledgerUnits(input.available) ?? 0n)) {
    return "Withdrawal exceeds withdrawable balance.";
  }
  if (input.destination === "alipay" && !input.paymentQrCode) {
    return "Payment QR code is required.";
  }
  return null;
}

export function buildAlipayWithdrawalTicketInput(input: {
  amount: string;
  note: string;
  paymentQrCode: string;
}): CreateTicketInput {
  return {
    ticketType: "general",
    title: "供应商提现申请",
    firstMessage: [
      `提现积分：${input.amount.trim()} 积分`,
      "提现方式：支付宝",
      `备注：${input.note.trim() || "无"}`,
    ].join("\n"),
    attachments: [input.paymentQrCode],
  };
}
