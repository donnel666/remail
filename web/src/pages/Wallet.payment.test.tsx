// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  createRecharge: vi.fn(),
  getRecharge: vi.fn(),
  getRechargeConfig: vi.fn(),
  getWallet: vi.fn(),
  getWalletReferrals: vi.fn(),
  listRecharges: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  toastWarning: vi.fn(),
  translate: (key: string) => key,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));

vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));

vi.mock("@/lib/wallet-api", () => ({
  createRecharge: mocks.createRecharge,
  getRecharge: mocks.getRecharge,
  getRechargeConfig: mocks.getRechargeConfig,
  getWallet: mocks.getWallet,
  getWalletReferrals: mocks.getWalletReferrals,
  listRecharges: mocks.listRecharges,
  redeemCard: vi.fn(),
  transferReferralRewards: vi.fn(),
}));

vi.mock("@/lib/iam-api", () => ({
  createMyInvite: vi.fn(),
  getMyInvite: vi.fn(async () => ({ inviteCode: "INVITE" })),
}));

vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("@douyinfe/semi-illustrations", () => ({ IllustrationNoResult: () => null, IllustrationNoResultDark: () => null }));

vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
  const Box = ({ children }: { children?: React.ReactNode }) => <div>{children}</div>;
  const Button = ({ children, disabled, loading, onClick }: any) => <button disabled={disabled || loading} onClick={onClick}>{children}</button>;
  const Card = ({ children, cover, title }: any) => <section>{title}{cover}{children}</section>;
  const Form = ({ children, getFormApi }: any) => {
    getFormApi?.({ setValue: vi.fn() });
    return <div>{children}</div>;
  };
  Form.InputNumber = ({ label, onChange }: any) => <input aria-label={label} onChange={(event) => onChange?.(event.target.value)} />;
  Form.Input = ({ onChange, placeholder }: any) => <input aria-label={placeholder} onChange={(event) => onChange?.(event.target.value)} />;
  Form.Slot = Box;
  const Input = ({ onChange, placeholder, value }: any) => <input aria-label={placeholder} onChange={(event) => onChange?.(event.target.value)} value={value} />;
  const Modal = ({ children, footer, onCancel, title, visible }: any) => visible ? <div aria-label={title} role="dialog"><button aria-label={`close-${title}`} onClick={onCancel}>close</button>{children}{footer}</div> : null;
  return {
    Avatar: Box,
    Button,
    Card,
    Empty: Box,
    Form,
    Input,
    Modal,
    Space: Box,
    Spin: () => <span>spinner</span>,
    Table: Box,
    Tag: Box,
    Toast: { error: mocks.toastError, success: mocks.toastSuccess, warning: mocks.toastWarning, info: vi.fn() },
    Typography: { Text: Box },
  };
});

import Wallet from "./Wallet";

let now = Date.parse("2026-07-26T00:00:00Z");
let paymentPoll: (() => void) | undefined;

const payingRecharge = {
  id: 1,
  rechargeNo: "RC00000000000000000000000000000001",
  userId: 1,
  paymentMethod: "alipay",
  rechargeQuota: "1.00",
  paymentAmount: "1.01",
  status: "paying",
  queryAttempts: 0,
  expiresAt: "2026-07-26T00:05:00Z",
  createdAt: "2026-07-26T00:00:00Z",
  updatedAt: "2026-07-26T00:00:00Z",
};

describe("wallet payment modal", () => {
  beforeEach(() => {
    now = Date.parse("2026-07-26T00:00:00Z");
    paymentPoll = undefined;
    vi.spyOn(Date, "now").mockImplementation(() => now);
    vi.spyOn(window, "setInterval").mockImplementation((handler, timeout) => {
      if (timeout === 2_500 && typeof handler === "function") paymentPoll = handler as () => void;
      return 1;
    });
    vi.spyOn(window, "clearInterval").mockImplementation(() => undefined);
    mocks.getWallet.mockResolvedValue({ consumerBalance: "0.00", supplierAvailable: "0.00", supplierFrozen: "0.00", historicalSpend: "0.00", orderCount: 0 });
    mocks.getWalletReferrals.mockResolvedValue({ inviteCount: 0, pendingRewards: "0.00", totalEarned: "0.00" });
    mocks.getRechargeConfig.mockResolvedValue({ enabled: true, minAmount: "1.00", feeRate: "0.6", feeCap: "0", tiers: [{ amount: "1.00", bonus: "0.00", rechargeQuota: "1.00", paymentAmount: "1.01" }] });
    mocks.listRecharges.mockResolvedValue({ items: [], total: 0, offset: 0, limit: 100 });
    mocks.getRecharge.mockResolvedValue(payingRecharge);
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it("shows the final iframe and reports a credited terminal state", async () => {
    mocks.createRecharge.mockResolvedValue({ recharge: payingRecharge, payUrl: "https://pay.example.com/qr", expiresAt: "2026-07-26T00:05:00Z" });
    render(<Wallet />);

    const payButton = await screen.findByRole("button", { name: "Alipay" });
    await waitFor(() => expect(payButton).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Billing" }));
    fireEvent.change(screen.getByLabelText("Order No."), { target: { value: "old-order" } });
    fireEvent.click(screen.getByRole("button", { name: "close-Recharge Billing" }));
    fireEvent.click(payButton);

    await waitFor(() => expect(document.querySelector("iframe")).not.toBeNull());
    const frame = document.querySelector("iframe")!;
    expect(screen.getByText("Loading payment page...")).toBeVisible();
    fireEvent.load(frame);
    expect(screen.queryByText("Loading payment page...")).not.toBeInTheDocument();

    await waitFor(() => expect(mocks.getRecharge).toHaveBeenCalledOnce());
    mocks.getRecharge.mockResolvedValue({ ...payingRecharge, status: "credited" });
    await act(async () => paymentPoll?.());

    expect(mocks.toastSuccess).toHaveBeenCalledWith("Recharge successful. Balance has been credited.");
    expect(screen.queryByTitle("Alipay Payment")).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Recharge Billing" })).toBeVisible();
    expect(screen.getByLabelText("Order No.")).toHaveValue("");
  });

  it("closes the payment page at the five-minute verification deadline", async () => {
    mocks.createRecharge.mockResolvedValue({ recharge: payingRecharge, payUrl: "https://pay.example.com/qr", expiresAt: "2026-07-26T00:00:01Z" });
    render(<Wallet />);

    const payButton = await screen.findByRole("button", { name: "Alipay" });
    await waitFor(() => expect(payButton).toBeEnabled());
    fireEvent.click(payButton);
    await waitFor(() => expect(document.querySelector("iframe")).not.toBeNull());
    now += 2_500;
    await act(async () => paymentPoll?.());

    expect(mocks.toastError).toHaveBeenCalledWith("Recharge verification timed out. Please check the billing record.");
    expect(document.querySelector("iframe")).toBeNull();
    expect(screen.getByRole("dialog", { name: "Recharge Billing" })).toBeVisible();
  });
});
