// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { getOrderAfterSaleState, type TicketOrderSource } from "./tickets-api";

describe("order warranty eligibility", () => {
  afterEach(() => vi.useRealTimers());

  it.each([
    ["active", "2026-10-11T00:00:00Z", false],
    ["active", "2026-10-12T00:00:00Z", true],
    ["completed", "2026-10-12T00:00:00Z", true],
    ["closed", "2026-10-10T00:00:00Z", false],
    ["refunded", "2026-10-12T00:00:00Z", false],
  ])("checks the deadline for %s orders", (status, deadline, eligible) => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-11T00:00:00Z"));
    const order: TicketOrderSource = {
      orderNo: "OR-WARRANTY", deliveryEmail: "mail@example.com", payAmount: "80.00",
      serviceMode: "purchase", status, afterSaleUntil: deadline,
    };
    expect(getOrderAfterSaleState(order).eligible).toBe(eligible);
    expect(getOrderAfterSaleState({ ...order, afterSaleUntil: null, receiveUntil: deadline }).eligible).toBe(eligible);
  });
});
