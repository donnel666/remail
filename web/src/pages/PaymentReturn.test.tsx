// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ getRecharge: vi.fn() }));

vi.mock("@/lib/wallet-api", () => ({ getRecharge: mocks.getRecharge }));

import PaymentReturn from "./PaymentReturn";

const rechargeNo = "RC00000000000000000000000000000001";
let parentDescriptor: PropertyDescriptor | undefined;
let postMessage: ReturnType<typeof vi.fn>;

describe("payment return page", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    parentDescriptor = Object.getOwnPropertyDescriptor(window, "parent");
    postMessage = vi.fn();
    Object.defineProperty(window, "parent", {
      configurable: true,
      value: { postMessage },
    });
    window.history.replaceState({}, "", `/payment/return?out_trade_no=${rechargeNo}`);
    mocks.getRecharge.mockResolvedValue({ status: "paying" });
  });

  afterEach(() => {
    cleanup();
    if (parentDescriptor) Object.defineProperty(window, "parent", parentDescriptor);
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it("checks every five seconds, shows help at 30 seconds, and closes at 60 seconds", async () => {
    render(<PaymentReturn />);
    expect(screen.getByText("60")).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent("正在确认到账");
    expect(mocks.getRecharge).toHaveBeenCalledOnce();

    await act(async () => undefined);
    act(() => vi.advanceTimersByTime(4999));
    expect(mocks.getRecharge).toHaveBeenCalledOnce();
    act(() => vi.advanceTimersByTime(1));
    expect(mocks.getRecharge).toHaveBeenCalledTimes(2);

    act(() => vi.advanceTimersByTime(25_000));
    expect(screen.getByText("30")).toBeVisible();
    expect(screen.getByText(/还没到账？别担心/)).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent("到账可能有延迟");

    act(() => vi.advanceTimersByTime(30_000));
    expect(postMessage).toHaveBeenCalledOnce();
    expect(postMessage).toHaveBeenCalledWith(
      "remail:epay-return",
      window.location.origin
    );
  });

  it("shows credited status for five seconds before closing", async () => {
    mocks.getRecharge.mockResolvedValue({ status: "credited" });
    render(<PaymentReturn />);

    await act(async () => undefined);

    expect(mocks.getRecharge).toHaveBeenCalledWith(rechargeNo);
    expect(screen.getByRole("heading", { name: "充值已到账" })).toBeVisible();
    expect(screen.getByText("5")).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent("充值已到账");
    expect(postMessage).not.toHaveBeenCalled();

    act(() => vi.advanceTimersByTime(5000));

    expect(postMessage).toHaveBeenCalledWith(
      "remail:epay-return",
      window.location.origin
    );
  });
});
