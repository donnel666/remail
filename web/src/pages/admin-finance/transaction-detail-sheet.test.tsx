// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import type { FinanceTransaction } from "./admin-finance-api";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/components/semi/copyable-table-text", () => ({ CopyableTableText: ({ text }: { text: string }) => <span>{text}</span> }));
vi.mock("./transaction-meta", () => ({ TransactionAccountCell: () => <span>Account</span> }));
vi.mock("@douyinfe/semi-ui", () => ({
  SideSheet: ({ children }: { children: ReactNode }) => <section>{children}</section>,
  Tag: ({ children }: { children: ReactNode }) => <span>{children}</span>,
  Button: ({ children, disabled, onClick }: { children: ReactNode; disabled?: boolean; onClick?: () => void }) => <button disabled={disabled} onClick={onClick}>{children}</button>,
}));

import { TransactionDetailSheet } from "./transaction-detail-sheet";

afterEach(cleanup);

describe("order payment reversal protection", () => {
  it("disables reversal for a ticket withdrawal", () => {
    const transaction: FinanceTransaction = {
      id: 2, transactionNo: "TX-WITHDRAWAL", userId: 7, userEmail: "supplier@example.com", userNickname: "Supplier",
      userRole: "supplier", userGroupName: "Default", transactionType: "withdrawal", balanceBucket: "supplier_available", direction: "out",
      amount: "-260000.00", balanceBefore: "500000.00", balanceAfter: "240000.00", bizType: "ticket_withdrawal",
      bizId: "AS-WITHDRAWAL", orderLinked: false, createdAt: "2026-10-23T00:00:00Z",
    };
    const onReverse = vi.fn();
    render(<TransactionDetailSheet transaction={transaction} onClose={() => {}} onReverse={onReverse} />);
    expect(screen.getByRole("button", { name: "Reverse" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Reverse" }));
    expect(onReverse).not.toHaveBeenCalled();
  });

  it.each(["debit", "refund"] as const)("disables reversal for an order-linked %s", (transactionType) => {
    const transaction: FinanceTransaction = {
      id: 1, transactionNo: "TX-ORDER", userId: 2, userEmail: "buyer@example.com", userNickname: "Buyer",
      userRole: "user", userGroupName: "Default", transactionType, balanceBucket: "consumer", direction: "out",
      amount: "-80.00", balanceBefore: "1000.00", balanceAfter: "920.00", bizType: "admin_wallet_adjustment",
      bizId: "legacy-note", orderLinked: true, createdAt: "2026-10-11T00:00:00Z",
    };
    const onReverse = vi.fn();
    const { rerender } = render(<TransactionDetailSheet transaction={transaction} onClose={() => {}} onReverse={onReverse} />);
    expect(screen.getByRole("button", { name: "Reverse" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Reverse" }));
    expect(onReverse).not.toHaveBeenCalled();
    rerender(<TransactionDetailSheet transaction={{ ...transaction, orderLinked: false }} onClose={() => {}} onReverse={onReverse} />);
    expect(screen.getByRole("button", { name: "Reverse" })).toBeEnabled();
  });
});
