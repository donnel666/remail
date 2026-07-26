// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getWallet: vi.fn(),
  listWalletTransactions: vi.fn(),
  toastError: vi.fn(),
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

vi.mock("./tickets/tickets-api", () => ({ createTicket: vi.fn() }));

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
    Toast: { error: mocks.toastError, success: vi.fn(), warning: vi.fn() },
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
  });

  afterEach(() => cleanup());

  it("keeps exact wallet data usable when transactions or a refresh fail", async () => {
    render(<FinanceCenter />);

    expect(await screen.findByText("¥999,999,999,999.999999")).toBeVisible();
    expect(screen.getByText("¥1,000,000,000,000.00")).toBeVisible();
    expect(screen.getByRole("button", { name: "Withdraw" })).toBeEnabled();
    expect(mocks.toastError).toHaveBeenCalledWith("Supplier transactions load failed.");

    mocks.getWallet.mockRejectedValueOnce(new Error("wallet unavailable"));
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));

    await waitFor(() => expect(mocks.getWallet).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("button", { name: "Withdraw" })).toBeEnabled());
    expect(screen.getByText("¥999,999,999,999.999999")).toBeVisible();
  });
});
