import { describe, expect, it } from "vitest";

import {
  toFinanceCardKey,
  toFinanceUserBalance,
  walletToFinanceBalance,
} from "./admin-finance-api";

// The finance summary/trend/reversal computation moved to the backend, so the
// former mock-computation assertions no longer apply. These cover the pure
// response mappers instead — the field-rename + role-cast logic that lives in
// the frontend api layer, which is the real bug surface after the swap.
describe("admin-finance-api response mappers", () => {
  it("maps a card response, renaming cardKey -> key and casting role", () => {
    const card = toFinanceCardKey({
      cardKey: "RM-ABC",
      amount: "100.00",
      status: "enabled",
      maxRedemptions: 5,
      redeemedCount: 2,
      expireAt: null,
      createdAt: "2026-01-01T00:00:00Z",
      updatedAt: "2026-01-02T00:00:00Z",
      ownerUserId: 7,
      ownerEmail: "owner@example.com",
      ownerNickname: "owner",
      ownerRole: "supplier",
      ownerGroupName: "VIP",
    });

    expect(card.key).toBe("RM-ABC");
    expect(card.ownerRole).toBe("supplier");
    expect(card.redeemedCount).toBe(2);
    expect(card.expireAt).toBeNull();
  });

  it("maps a wallet item, renaming user* fields to the plain balance shape", () => {
    const balance = toFinanceUserBalance({
      userId: 3,
      userEmail: "user@example.com",
      userNickname: "nn",
      userRole: "admin",
      userGroupName: "代理商",
      consumerBalance: "12.00",
      supplierAvailable: "3.00",
      supplierFrozen: "0.00",
      updatedAt: "2026-01-03T00:00:00Z",
    });

    expect(balance.email).toBe("user@example.com");
    expect(balance.nickname).toBe("nn");
    expect(balance.role).toBe("admin");
    expect(balance.groupName).toBe("代理商");
    expect(balance.consumerBalance).toBe("12.00");
  });

  it("maps a bucket-only wallet with blank enrichment for the panel merge", () => {
    const balance = walletToFinanceBalance({
      userId: 9,
      consumerBalance: "50.00",
      supplierAvailable: "10.00",
      supplierFrozen: "1.00",
      historicalSpend: "0.00",
      orderCount: 0,
      updatedAt: "2026-01-04T00:00:00Z",
    });

    expect(balance.userId).toBe(9);
    expect(balance.consumerBalance).toBe("50.00");
    expect(balance.supplierAvailable).toBe("10.00");
    // Enrichment is intentionally blank; the balances panel keeps the existing
    // row's identity and only overwrites the buckets.
    expect(balance.email).toBe("");
    expect(balance.groupName).toBe("");
  });
});
