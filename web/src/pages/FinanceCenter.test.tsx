// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getWallet: vi.fn(),
  listWalletTransactions: vi.fn(),
  transferSupplierBalance: vi.fn(),
  createSupplierWithdrawal: vi.fn(),
  requireTurnstile: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  toastWarning: vi.fn(),
  translate: (key: string) => key,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));

vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({ currentUser: { role: "supplier" } }),
}));

vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));

vi.mock("@/components/auth/TurnstileGate", () => ({
  requireTurnstile: mocks.requireTurnstile,
}));

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));

vi.mock("@/lib/wallet-api", () => ({
  createSupplierWithdrawal: mocks.createSupplierWithdrawal,
  getWallet: mocks.getWallet,
  listWalletTransactions: mocks.listWalletTransactions,
  transferSupplierBalance: mocks.transferSupplierBalance,
}));

vi.mock("@/components/semi/card-table", () => ({
  CardTable: ({ dataSource, loading }: any) => (
    <div data-loading={String(loading)} data-testid="transactions">
      {dataSource.length}
    </div>
  ),
}));

vi.mock("./resources/supplier-application-modal", () => ({
  createSupplierApplicationTicket: vi.fn(),
  hasSupplierRole: (role?: string | null) =>
    role === "supplier" || role === "admin" || role === "super_admin",
}));

vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
  const { default: SemiModal } = await import("@douyinfe/semi-ui/lib/es/modal");
  const Box = ({ children }: { children?: React.ReactNode }) => <div>{children}</div>;
  const Button = ({ children, disabled, loading, onClick, ...props }: any) => (
    <button
      aria-label={props["aria-label"]}
      disabled={disabled || loading}
      onClick={onClick}
      type="button"
    >
      {children}
    </button>
  );
  const Card = ({ children, title }: any) => (
    <section>
      {title}
      {children}
    </section>
  );
  const InputNumber = ({ autoFocus, id, onChange, value }: any) => (
    <input autoFocus={autoFocus} id={id} onChange={(event) => onChange?.(event.target.value)} value={value} />
  );
  const Modal = (props: any) => <SemiModal {...props} motion={false} />;
  const Radio = ({ children }: any) => <label>{children}</label>;
  const RadioGroup = ({ children }: any) => <div>{children}</div>;
  const Skeleton = ({ children, loading }: any) =>
    loading ? <span>loading</span> : <>{children}</>;
  Skeleton.Paragraph = () => null;
  const TextArea = ({ id, onChange, value }: any) => (
    <textarea id={id} onChange={(event) => onChange?.(event.target.value)} value={value} />
  );
  return {
    Avatar: Box,
    Button,
    Card,
    Empty: Box,
    InputNumber,
    Modal,
    Radio,
    RadioGroup,
    Skeleton,
    Space: Box,
    Tag: Box,
    TextArea,
    Toast: { error: mocks.toastError, success: mocks.toastSuccess, warning: mocks.toastWarning },
  };
});

import FinanceCenter from "./FinanceCenter";

const wallet = {
  userId: 1,
  consumerBalance: "10.00",
  supplierAvailable: "999999999999.999999",
  supplierFrozen: "0.000001",
  historicalSpend: "0.00",
  orderCount: 0,
  supplierAllocationCount: 42,
  supplierFulfillmentSuccessRate: 75,
  updatedAt: "2026-07-26T00:00:00Z",
};

describe("FinanceCenter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.getWallet.mockResolvedValue(wallet);
    mocks.listWalletTransactions.mockRejectedValue(new Error("transactions unavailable"));
    mocks.transferSupplierBalance.mockResolvedValue({
      ...wallet,
      consumerBalance: "15.00",
      supplierAvailable: "999999999994.999999",
    });
    mocks.createSupplierWithdrawal.mockResolvedValue({});
    mocks.requireTurnstile.mockResolvedValue("turnstile-token");
  });

  afterEach(() => cleanup());

  it("keeps exact wallet data usable when transactions or a refresh fail", async () => {
    render(<FinanceCenter />);

    expect((await screen.findAllByText("1T")).length).toBeGreaterThanOrEqual(2);
    expect(screen.getByRole("button", { name: "Withdraw to Alipay" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Transfer to user wallet" })).toBeEnabled();
    expect(screen.getByText("Allocation count")).toBeVisible();
    expect(screen.getByText("42")).toBeVisible();
    expect(screen.getByText("Order fulfillment success rate")).toBeVisible();
    expect(screen.getByText("75%")).toBeVisible();
    expect(mocks.toastError).toHaveBeenCalledWith("Supplier transactions load failed.");

    fireEvent.click(screen.getByRole("button", { name: "Withdraw to Alipay" }));
    fireEvent.click(screen.getByRole("button", { name: "I understand, continue withdrawal" }));
    expect(screen.getByText(/999,999,999,999\.999999/)).toBeVisible();

    mocks.getWallet.mockRejectedValueOnce(new Error("wallet unavailable"));
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));

    await waitFor(() => expect(mocks.getWallet).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Withdraw to Alipay" })).toBeEnabled(),
    );
    expect(screen.getAllByText("1T").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText(/999,999,999,999\.999999/)).toBeVisible();
  });

  it("transfers supplier balance directly instead of creating a ticket", async () => {
    render(<FinanceCenter />);

    fireEvent.click(await screen.findByRole("button", { name: "Transfer to user wallet" }));
    expect(screen.getByRole("dialog", { name: "Transfer supplier balance" })).toBeVisible();
    expect(screen.queryByText("Alipay payment QR code")).not.toBeInTheDocument();
    expect(screen.queryByText("Alipay withdrawals are subject to a 3% fee.")).not.toBeInTheDocument();

    fireEvent.change(screen.getByRole("textbox"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm transfer" }));

    await waitFor(() =>
      expect(mocks.transferSupplierBalance).toHaveBeenCalledWith(
        "5",
        expect.any(String),
        "turnstile-token",
      ),
    );
    expect(mocks.createSupplierWithdrawal).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Wallet transfer completed.");
  });

  it("requires the fee and risk notice before opening and submitting an Alipay withdrawal", async () => {
    render(<FinanceCenter />);

    fireEvent.click(await screen.findByRole("button", { name: "Withdraw to Alipay" }));
    const dialog = screen.getByRole("dialog", { name: "Alipay withdrawal notice" });
    expect(dialog).toBeVisible();
    expect(document.getElementById("withdraw-amount")).not.toBeInTheDocument();
    expect(mocks.requireTurnstile).not.toHaveBeenCalled();
    expect(mocks.createSupplierWithdrawal).not.toHaveBeenCalled();
    expect(screen.getByText("Alipay withdrawals are subject to a 3% fee.")).toBeVisible();
    expect(screen.getByText(/Upload a clear, valid Alipay payment QR code belonging to you/)).toBeVisible();
    expect(screen.getByText(/You are responsible for any failed or delayed transfers or financial losses/)).toBeVisible();

    const continueButton = screen.getByRole("button", { name: "I understand, continue withdrawal" });
    continueButton.focus();
    fireEvent.click(continueButton);
    expect(screen.getByRole("dialog", { name: "Supplier withdrawal application" })).toBe(dialog);
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(document.getElementById("withdraw-amount")).toHaveFocus();
    expect(mocks.createSupplierWithdrawal).not.toHaveBeenCalled();
    fireEvent.change(document.getElementById("withdraw-amount")!, { target: { value: "5" } });
    fireEvent.change(document.querySelector('input[type="file"]')!, {
      target: { files: [new File(["qr"], "qr.png", { type: "image/png" })] },
    });
    fireEvent.change(document.getElementById("withdraw-note")!, { target: { value: "please process" } });
    await screen.findByAltText("Alipay payment QR code");
    fireEvent.click(screen.getByRole("button", { name: "Submit" }));

    await waitFor(() =>
      expect(mocks.createSupplierWithdrawal).toHaveBeenCalledWith(
        "5",
        "please process",
        expect.stringMatching(/^data:image\/png;base64,/),
        "turnstile-token",
      ),
    );
    expect(mocks.transferSupplierBalance).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Withdrawal submitted.");
  });

  it("cancels the notice and requires confirmation again when reopened", async () => {
    render(<FinanceCenter />);

    fireEvent.click(await screen.findByRole("button", { name: "Withdraw to Alipay" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mocks.requireTurnstile).not.toHaveBeenCalled();
    expect(mocks.createSupplierWithdrawal).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Withdraw to Alipay" }));
    expect(screen.getByRole("dialog", { name: "Alipay withdrawal notice" })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "I understand, continue withdrawal" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    fireEvent.click(screen.getByRole("button", { name: "Withdraw to Alipay" }));
    expect(screen.getByRole("dialog", { name: "Alipay withdrawal notice" })).toBeVisible();
    expect(document.getElementById("withdraw-amount")).not.toBeInTheDocument();
  });

  it.each(["notice", "form"])("removes the %s dialog when the page unmounts", async (step) => {
    const { unmount } = render(<FinanceCenter />);

    fireEvent.click(await screen.findByRole("button", { name: "Withdraw to Alipay" }));
    if (step === "form") {
      fireEvent.click(screen.getByRole("button", { name: "I understand, continue withdrawal" }));
    }
    expect(screen.getByRole("dialog")).toBeVisible();
    unmount();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(mocks.requireTurnstile).not.toHaveBeenCalled();
    expect(mocks.createSupplierWithdrawal).not.toHaveBeenCalled();
  });

  it("reuses the transfer idempotency key after an ambiguous failure", async () => {
    mocks.transferSupplierBalance.mockRejectedValueOnce(new Error("response lost"));
    render(<FinanceCenter />);

    fireEvent.click(await screen.findByRole("button", { name: "Transfer to user wallet" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm transfer" }));

    await waitFor(() => expect(mocks.transferSupplierBalance).toHaveBeenCalledTimes(1));
    const firstKey = mocks.transferSupplierBalance.mock.calls[0][1];
    expect(firstKey).toEqual(expect.any(String));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Confirm transfer" })).toBeEnabled(),
    );

    fireEvent.click(screen.getByRole("button", { name: "Confirm transfer" }));

    await waitFor(() => expect(mocks.transferSupplierBalance).toHaveBeenCalledTimes(2));
    expect(mocks.transferSupplierBalance.mock.calls[1]).toEqual([
      "5",
      firstKey,
      "turnstile-token",
    ]);
  });

  it("does not submit fractional supplier points", async () => {
    render(<FinanceCenter />);

    fireEvent.click(await screen.findByRole("button", { name: "Transfer to user wallet" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "5.25" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm transfer" }));

    expect(mocks.transferSupplierBalance).not.toHaveBeenCalled();
    expect(mocks.createSupplierWithdrawal).not.toHaveBeenCalled();
    expect(mocks.toastWarning).toHaveBeenCalledWith("Amount must be an integer.");
  });
});
