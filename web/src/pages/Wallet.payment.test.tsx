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
  listWalletTransactions: vi.fn(),
  quoteRecharge: vi.fn(),
  refreshCurrentUser: vi.fn(),
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
  listWalletTransactions: mocks.listWalletTransactions,
  quoteRecharge: mocks.quoteRecharge,
  redeemCard: vi.fn(),
  transferReferralRewards: vi.fn(),
}));

vi.mock("@/lib/iam-api", () => ({
  IamApiError: class IamApiError extends Error {},
  createMyInvite: vi.fn(),
  getMyInvite: vi.fn(async () => ({ inviteCode: "INVITE" })),
  getUserGroups: vi.fn(async () => ({ groups: [] })),
}));

vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({
    currentUser: {
      userGroup: {
        id: 1,
        code: "normal",
        name: "Normal",
        description: "",
        enabled: true,
        apiConcurrencyLimit: 3,
        priceDiscountRatio: "1.00",
        topupThreshold: "0.00",
        autoUpgradeEnabled: false,
      },
    },
    refreshCurrentUser: mocks.refreshCurrentUser,
  }),
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
  Form.Input = ({ onChange, placeholder, prefix, suffix }: any) => <div>{prefix}<input aria-label={placeholder} onChange={(event) => onChange?.(event.target.value)} />{suffix}</div>;
  Form.Slot = Box;
  const Input = ({ onChange, placeholder, value }: any) => <input aria-label={placeholder} onChange={(event) => onChange?.(event.target.value)} value={value} />;
  const Modal = ({ children, footer, onCancel, title, visible }: any) => visible ? <div aria-label={title} role="dialog"><button aria-label={`close-${title}`} onClick={onCancel}>close</button>{children}{footer}</div> : null;
  const Table = ({ columns = [], dataSource = [] }: any) => <div>{dataSource.map((item: any) => <div data-testid={`row-${item.orderNo}`} key={item.orderNo}>{columns.map((column: any) => <span key={column.key}>{column.render ? column.render(item[column.dataIndex], item) : item[column.dataIndex]}</span>)}</div>)}</div>;
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
    Table,
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
  creditedPoints: "1000.00",
  status: "paying",
  queryAttempts: 0,
  expiresAt: "2026-07-26T00:05:00Z",
  createdAt: "2026-07-26T00:00:00Z",
  updatedAt: "2026-07-26T00:00:00Z",
};

function redemptionTransaction(id: number, transactionNo: string) {
  return {
    id,
    transactionNo,
    userId: 1,
    transactionType: "card_redeem",
    balanceBucket: "consumer",
    direction: "in",
    amount: "25.00",
    balanceBefore: "0.00",
    balanceAfter: "25.00",
    bizType: "card_key",
    bizId: `GIFT-CODE-${id}`,
    createdAt: `2026-07-26T00:0${id}:00Z`,
  };
}

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
    mocks.getWallet.mockResolvedValue({ consumerBalance: "0.00", supplierAvailable: "0.00", supplierFrozen: "0.00", totalRecharged: "0.00", historicalSpend: "0.00", orderCount: 0 });
    mocks.getWalletReferrals.mockResolvedValue({ inviteCount: 0, pendingRewards: "0.00", totalEarned: "0.00" });
    mocks.getRechargeConfig.mockResolvedValue({ enabled: true, minPoints: "1000.00", feeRate: "0.6", feeCapPoints: "0", tiers: [{ points: "1000.00", bonusPoints: "0.00", feePoints: "6.00", creditedPoints: "1000.00" }] });
    mocks.quoteRecharge.mockResolvedValue({ points: "1000.00", bonusPoints: "0.00", feePoints: "6.00", creditedPoints: "1000.00" });
    mocks.listRecharges.mockResolvedValue({ items: [], total: 0, offset: 0, limit: 100 });
    mocks.listWalletTransactions.mockResolvedValue({ items: [], hasNext: false, limit: 100 });
    mocks.getRecharge.mockResolvedValue(payingRecharge);
    mocks.refreshCurrentUser.mockResolvedValue(null);
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it("lets the return page check while open, then confirms after it closes", async () => {
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

    expect(mocks.getRecharge).not.toHaveBeenCalled();
    mocks.getRecharge.mockResolvedValue({ ...payingRecharge, status: "credited" });

    fireEvent(
      window,
      new MessageEvent("message", {
        data: "remail:epay-return",
        origin: window.location.origin,
        source: frame.contentWindow,
      })
    );
    expect(document.querySelector("iframe")).toBeNull();
    expect(screen.getByRole("dialog", { name: "Recharge Billing" })).toBeVisible();

    await waitFor(() => expect(mocks.getRecharge).toHaveBeenCalledOnce());
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledWith("Recharge successful. Balance has been credited."));
    expect(screen.queryByTitle("Alipay Payment")).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Recharge Billing" })).toBeVisible();
    expect(screen.getByLabelText("Order No.")).toHaveValue("");
    expect(mocks.refreshCurrentUser).toHaveBeenCalledTimes(2);
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

  it("checks a credited result before applying the verification deadline", async () => {
    mocks.createRecharge.mockResolvedValue({ recharge: payingRecharge, payUrl: "https://pay.example.com/qr", expiresAt: "2026-07-26T00:00:01Z" });
    mocks.getRecharge.mockResolvedValue({ ...payingRecharge, status: "credited" });
    render(<Wallet />);

    const payButton = await screen.findByRole("button", { name: "Alipay" });
    await waitFor(() => expect(payButton).toBeEnabled());
    fireEvent.click(payButton);
    await waitFor(() => expect(document.querySelector("iframe")).not.toBeNull());

    now += 2_500;
    await act(async () => paymentPoll?.());

    expect(mocks.getRecharge).toHaveBeenCalled();
    expect(mocks.toastError).not.toHaveBeenCalled();
    expect(document.querySelector("iframe")).not.toBeNull();
  });

  it("shows redemption code transactions in billing", async () => {
    mocks.listWalletTransactions.mockResolvedValue({
      items: [redemptionTransaction(2, "TX0002")],
      hasNext: false,
      limit: 100,
    });
    const { container } = render(<Wallet />);

    fireEvent.click(await screen.findByRole("button", { name: "Billing" }));

    const row = await screen.findByTestId("row-TX0002");
    expect(row).toHaveTextContent("Redemption Code");
    expect(row.textContent?.match(/25 Points/g)).toHaveLength(1);
    expect(container.querySelector(".lucide-gift")?.parentElement).toHaveClass(
      "w-6",
      "justify-start",
    );
  });

  it("keeps recharge records when redemption history fails", async () => {
    mocks.listRecharges.mockResolvedValue({
      items: [{ ...payingRecharge, status: "credited" }],
      total: 1,
      offset: 0,
      limit: 100,
    });
    mocks.listWalletTransactions.mockRejectedValue(new Error("transactions unavailable"));
    render(<Wallet />);

    fireEvent.click(await screen.findByRole("button", { name: "Billing" }));

    expect(await screen.findByTestId(`row-${payingRecharge.rechargeNo}`)).toBeVisible();
    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("transactions unavailable"));
  });

  it("loads older redemption transactions with the wallet cursor", async () => {
    mocks.listWalletTransactions
      .mockResolvedValueOnce({
        items: [redemptionTransaction(2, "TX0002")],
        nextAfterId: 2,
        hasNext: true,
        limit: 100,
      })
      .mockResolvedValueOnce({
        items: [redemptionTransaction(1, "TX0001")],
        hasNext: false,
        limit: 100,
      });
    render(<Wallet />);

    fireEvent.click(await screen.findByRole("button", { name: "Billing" }));
    await screen.findByTestId("row-TX0002");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));

    expect(await screen.findByTestId("row-TX0001")).toBeVisible();
    expect(mocks.listWalletTransactions).toHaveBeenNthCalledWith(
      2,
      { search: undefined, type: "card_redeem" },
      2,
      100,
    );
  });

  it("does not let a recharge refresh cancel redemption history", async () => {
    let resolveTransactions!: (value: {
      items: ReturnType<typeof redemptionTransaction>[];
      hasNext: boolean;
      limit: number;
    }) => void;
    const transactions = new Promise<{
      items: ReturnType<typeof redemptionTransaction>[];
      hasNext: boolean;
      limit: number;
    }>((resolve) => {
      resolveTransactions = resolve;
    });
    mocks.createRecharge.mockResolvedValue({
      recharge: payingRecharge,
      payUrl: "https://pay.example.com/qr",
      expiresAt: "2026-07-26T00:05:00Z",
    });
    mocks.listWalletTransactions.mockReturnValueOnce(transactions);
    render(<Wallet />);

    const payButton = await screen.findByRole("button", { name: "Alipay" });
    await waitFor(() => expect(payButton).toBeEnabled());
    fireEvent.click(await screen.findByRole("button", { name: "Billing" }));
    await waitFor(() => expect(mocks.listWalletTransactions).toHaveBeenCalledOnce());
    fireEvent.click(payButton);
    await waitFor(() => expect(mocks.listRecharges).toHaveBeenCalledTimes(2));
    resolveTransactions({
      items: [redemptionTransaction(2, "TX0002")],
      hasNext: false,
      limit: 100,
    });

    expect(await screen.findByTestId("row-TX0002")).toBeVisible();
  });
});
