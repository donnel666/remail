// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getTicket: vi.fn(),
  markTicketRead: vi.fn(),
  withdrawAndCloseTicket: vi.fn(),
  closeTicket: vi.fn(),
  getWallet: vi.fn(),
  confirm: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  success: vi.fn(),
  copied: vi.fn(),
  canReadWallet: true,
  translate: (key: string, values?: Record<string, unknown>) => values ? `${key}: ${Object.values(values).join(" / ")}` : key,
}));

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({ currentUser: { id: 42, name: "Admin" } }),
  hasPermissionKey: () => mocks.canReadWallet,
}));
vi.mock("../admin-users/admin-users-api", () => ({ getAdminUserWallet: mocks.getWallet }));
vi.mock("./tickets-api", () => ({
  getTicket: mocks.getTicket,
  markTicketRead: mocks.markTicketRead,
  withdrawAndCloseTicket: mocks.withdrawAndCloseTicket,
  closeTicket: mocks.closeTicket,
  refundAndCloseTicket: vi.fn(),
  replyTicket: vi.fn(),
}));
vi.mock("@douyinfe/semi-ui", () => {
  const Box = ({ children }: any) => <div>{children}</div>;
  return {
    Avatar: Box,
    Button: ({ children, onClick, loading, disabled }: any) => <button disabled={loading || disabled} onClick={onClick}>{children}</button>,
    Image: ({ src, alt }: any) => <img src={src} alt={alt} />,
    Modal: { confirm: mocks.confirm },
    SideSheet: ({ visible, children, title }: any) => visible ? <div role="dialog">{title}{children}</div> : null,
    Spin: () => <div>Loading</div>,
    Tag: Box,
    TextArea: ({ value, onChange }: any) => <textarea value={value} onChange={(event) => onChange(event.target.value)} />,
    Toast: { error: mocks.error, warning: mocks.warning, success: mocks.success },
    Tooltip: Box,
    Typography: { Text: ({ children, copyable }: any) => <span>{children}{copyable ? <button aria-label={`Copy ${copyable.content}`} onClick={() => mocks.copied(copyable.content)}>Copy</button> : null}</span> },
  };
});

import { TicketDetailSheet } from "./ticket-detail-sheet";
import type { Ticket } from "./tickets-api";
import { IamApiError } from "@/lib/api-client";

function withdrawalTicket(): Ticket {
  return {
    id: 1, ticketNo: "AS-WITHDRAWAL", ticketType: "general", title: "供应商提现申请", status: "open",
    requesterUserId: 7, requesterName: "Supplier", requesterEmail: "supplier@example.com",
    requesterRole: "supplier", requesterGroupName: "Default", requesterUnreadCount: 0, platformUnreadCount: 1,
    withdrawalAmount: "260000", createdAt: "2026-10-23T06:19:00Z", updatedAt: "2026-10-23T06:19:00Z",
    messages: [{ id: 1, senderType: "system", senderName: "系统", content: "提现积分：260000\n提现方式：支付宝\n备注：无", createdAt: "2026-10-23T06:19:00Z", attachments: ["/v1/tickets/AS-WITHDRAWAL/attachments/QR"] }],
  };
}

const wallet = { userId: 7, consumerBalance: "123.50", supplierAvailable: "500000", supplierFrozen: "0", historicalSpend: "0", orderCount: 0, updatedAt: "2026-10-23T06:19:00Z" };
const renderDetail = (props = {}) => render(<TicketDetailSheet ticketNo="AS-WITHDRAWAL" viewerRole="platform" platformCanOperate platformCanWithdraw onClose={vi.fn()} onChanged={vi.fn()} {...props} />);

beforeEach(() => {
  vi.clearAllMocks();
  mocks.canReadWallet = true;
  mocks.getTicket.mockResolvedValue(withdrawalTicket());
  mocks.getWallet.mockResolvedValue(wallet);
  mocks.markTicketRead.mockResolvedValue(undefined);
});
afterEach(cleanup);

describe("withdrawal ticket details", () => {
  it("renders system-message QR images, copyable requester data and both balances", async () => {
    renderDetail();
    expect(await screen.findByAltText("Alipay payment QR code")).toHaveAttribute("src", "/v1/tickets/AS-WITHDRAWAL/attachments/QR");
    expect(await screen.findByText("500,000")).toBeVisible();
    expect(screen.getByText("123.5")).toBeVisible();
    expect(screen.getByText("260,000")).toBeVisible();
    for (const value of ["7", "Supplier", "supplier@example.com"]) {
      fireEvent.click(screen.getByRole("button", { name: `Copy ${value}` }));
      expect(mocks.copied).toHaveBeenCalledWith(value);
    }
    expect(mocks.getWallet).toHaveBeenCalledWith(7);
  });

  it("confirms the account and original amount before withdrawing, then refreshes the balance", async () => {
    const onChanged = vi.fn();
    const closed: Ticket = { ...withdrawalTicket(), status: "closed", resolution: { kind: "withdrawn" }, updatedAt: "2026-10-23T06:20:00Z" };
    mocks.withdrawAndCloseTicket.mockResolvedValue(closed);
    renderDetail({ onChanged });
    fireEvent.click(await screen.findByRole("button", { name: "Confirm withdrawal and close" }));
    expect(mocks.withdrawAndCloseTicket).not.toHaveBeenCalled();
    const confirm = mocks.confirm.mock.calls[0][0];
    expect(confirm.content).toContain("Supplier / 7 / 260,000");
    expect(confirm.okText).toBe("Deduct points and close");
    mocks.getWallet.mockResolvedValue({ ...wallet, supplierAvailable: "240000" });
    await confirm.onOk();
    expect(mocks.withdrawAndCloseTicket).toHaveBeenCalledExactlyOnceWith("AS-WITHDRAWAL");
    expect(await screen.findByText("240,000")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Confirm withdrawal and close" })).not.toBeInTheDocument();
    expect(onChanged).toHaveBeenCalledOnce();
    expect(mocks.success).toHaveBeenCalledWith("Withdrawal deducted points: 260,000 / 7");
  });

  it("keeps the ticket open and displays the server error when the balance is insufficient", async () => {
    mocks.withdrawAndCloseTicket.mockRejectedValue(new IamApiError(422, { message: "Withdrawal exceeds withdrawable balance." }));
    renderDetail();
    fireEvent.click(await screen.findByRole("button", { name: "Confirm withdrawal and close" }));
    await mocks.confirm.mock.calls[0][0].onOk();
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Withdrawal exceeds withdrawable balance.: Ticket operation failed."));
    expect(screen.getByRole("button", { name: "Confirm withdrawal and close" })).toBeEnabled();
  });

  it.each([
    ["permission missing", { platformCanWithdraw: false }, withdrawalTicket()],
    ["ordinary ticket", {}, { ...withdrawalTicket(), withdrawalAmount: undefined }],
    ["closed ticket", {}, { ...withdrawalTicket(), status: "closed" }],
    ["requester view", { viewerRole: "user" }, withdrawalTicket()],
  ])("hides the withdrawal action for %s", async (_label, props, ticket) => {
    mocks.getTicket.mockResolvedValue(ticket);
    renderDetail(props);
    await screen.findByText("供应商提现申请");
    expect(screen.queryByRole("button", { name: "Confirm withdrawal and close" })).not.toBeInTheDocument();
  });

  it("does not fetch wallet data without wallet read permission", async () => {
    mocks.canReadWallet = false;
    renderDetail();
    await screen.findByRole("button", { name: "Copy 7" });
    expect(mocks.getWallet).not.toHaveBeenCalled();
    expect(screen.queryByText("Wallet points")).not.toBeInTheDocument();
  });

  it("reconciles a lost response without issuing another withdrawal", async () => {
    renderDetail();
    fireEvent.click(await screen.findByRole("button", { name: "Confirm withdrawal and close" }));
    mocks.withdrawAndCloseTicket.mockRejectedValue(new Error("network timeout"));
    mocks.getTicket.mockResolvedValue({ ...withdrawalTicket(), status: "closed", resolution: { kind: "withdrawn" } });
    await mocks.confirm.mock.calls[0][0].onOk();
    expect(mocks.withdrawAndCloseTicket).toHaveBeenCalledOnce();
    expect(mocks.warning).toHaveBeenCalledWith("Withdrawal already recorded points: 260,000 / 7");
    expect(mocks.error).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("button", { name: "Confirm withdrawal and close" })).not.toBeInTheDocument());
  });

  it("reports an unknown outcome when both submission and status lookup fail", async () => {
    renderDetail();
    fireEvent.click(await screen.findByRole("button", { name: "Confirm withdrawal and close" }));
    mocks.withdrawAndCloseTicket.mockRejectedValue(new Error("network timeout"));
    mocks.getTicket.mockRejectedValue(new Error("offline"));
    await mocks.confirm.mock.calls[0][0].onOk();
    expect(mocks.withdrawAndCloseTicket).toHaveBeenCalledOnce();
    expect(mocks.error).toHaveBeenCalledWith("Withdrawal result is unknown. Refresh the ticket and check the ledger before proceeding.");
    expect(mocks.success).not.toHaveBeenCalled();
  });

  it("does not claim a timed out withdrawal failed while the ticket is still open", async () => {
    renderDetail();
    fireEvent.click(await screen.findByRole("button", { name: "Confirm withdrawal and close" }));
    mocks.withdrawAndCloseTicket.mockRejectedValue(new Error("network timeout"));
    await mocks.confirm.mock.calls[0][0].onOk();
    expect(mocks.error).toHaveBeenCalledWith("Withdrawal result is unknown. Refresh the ticket and check the ledger before proceeding.");
    expect(mocks.withdrawAndCloseTicket).toHaveBeenCalledOnce();
  });

  it("does not replace another ticket with a late withdrawal response", async () => {
    let finish!: (ticket: Ticket) => void;
    mocks.withdrawAndCloseTicket.mockReturnValue(new Promise<Ticket>((resolve) => { finish = resolve; }));
    const detail = renderDetail();
    fireEvent.click(await screen.findByRole("button", { name: "Confirm withdrawal and close" }));
    const pending = mocks.confirm.mock.calls[0][0].onOk();
    mocks.getTicket.mockResolvedValue({ ...withdrawalTicket(), ticketNo: "AS-OTHER", requesterName: "Another supplier" });
    detail.rerender(<TicketDetailSheet ticketNo="AS-OTHER" viewerRole="platform" onClose={vi.fn()} onChanged={vi.fn()} />);
    await screen.findByText("Another supplier");
    finish({ ...withdrawalTicket(), status: "closed", resolution: { kind: "withdrawn" } });
    await pending;
    expect(screen.getByText("Another supplier")).toBeVisible();
    expect(screen.queryByText("Supplier")).not.toBeInTheDocument();
  });
});
