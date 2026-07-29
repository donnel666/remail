// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getWallet: vi.fn(),
  listWalletTransactions: vi.fn(),
  transferSupplierBalance: vi.fn(),
  createTicket: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  translate: (key: string) => key,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));

vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({ currentUser: { role: "supplier" } }),
}));

vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));

vi.mock("@/lib/wallet-api", () => ({
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

vi.mock("./tickets/tickets-api", () => ({ createTicket: mocks.createTicket }));

vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
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
  const Input = ({ id, onChange, value }: any) => (
    <input id={id} onChange={(event) => onChange?.(event.target.value)} value={value} />
  );
  const Modal = ({ children, footer, title, visible }: any) =>
    visible ? (
      <div aria-label={title} role="dialog">
        {children}
        {footer}
      </div>
    ) : null;
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
    Input,
    Modal,
    Radio,
    RadioGroup,
    Skeleton,
    Space: Box,
    Tag: Box,
    TextArea,
    Toast: { error: mocks.toastError, success: mocks.toastSuccess, warning: vi.fn() },
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
  updatedAt: "2026-07-26T00:00:00Z",
};

describe("FinanceCenter", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.getWallet.mockResolvedValue(wallet);
    mocks.listWalletTransactions.mockRejectedValue(new Error("transactions unavailable"));
    mocks.transferSupplierBalance.mockResolvedValue({
      ...wallet,
      consumerBalance: "15.25",
      supplierAvailable: "999999999994.749999",
    });
  });

  afterEach(() => cleanup());

  it("keeps exact wallet data usable when transactions or a refresh fail", async () => {
    render(<FinanceCenter />);

    expect(await screen.findByText("999,999,999,999.999999 Points")).toBeVisible();
    expect(screen.getByText("1,000,000,000,000 Points")).toBeVisible();
    expect(screen.getByRole("button", { name: "Withdraw to Alipay" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Transfer to user wallet" })).toBeEnabled();
    expect(mocks.toastError).toHaveBeenCalledWith("Supplier transactions load failed.");

    mocks.getWallet.mockRejectedValueOnce(new Error("wallet unavailable"));
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));

    await waitFor(() => expect(mocks.getWallet).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Withdraw to Alipay" })).toBeEnabled(),
    );
    expect(screen.getByText("999,999,999,999.999999 Points")).toBeVisible();
  });

  it("transfers supplier balance directly instead of creating a ticket", async () => {
    render(<FinanceCenter />);

    fireEvent.click(await screen.findByRole("button", { name: "Transfer to user wallet" }));
    expect(screen.getByRole("dialog", { name: "Transfer supplier balance" })).toBeVisible();
    expect(screen.queryByText("Alipay payment QR code")).not.toBeInTheDocument();

    fireEvent.change(screen.getByRole("textbox"), { target: { value: "5.25" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm transfer" }));

    await waitFor(() =>
      expect(mocks.transferSupplierBalance).toHaveBeenCalledWith("5.25", expect.any(String)),
    );
    expect(mocks.createTicket).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Wallet transfer completed.");
  });

  it("reuses the transfer idempotency key after an ambiguous failure", async () => {
    mocks.transferSupplierBalance.mockRejectedValueOnce(new Error("response lost"));
    render(<FinanceCenter />);

    fireEvent.click(await screen.findByRole("button", { name: "Transfer to user wallet" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "5.25" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm transfer" }));

    await waitFor(() => expect(mocks.transferSupplierBalance).toHaveBeenCalledTimes(1));
    const firstKey = mocks.transferSupplierBalance.mock.calls[0][1];
    expect(firstKey).toEqual(expect.any(String));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Confirm transfer" })).toBeEnabled(),
    );

    fireEvent.click(screen.getByRole("button", { name: "Confirm transfer" }));

    await waitFor(() => expect(mocks.transferSupplierBalance).toHaveBeenCalledTimes(2));
    expect(mocks.transferSupplierBalance.mock.calls[1]).toEqual(["5.25", firstKey]);
  });
});
