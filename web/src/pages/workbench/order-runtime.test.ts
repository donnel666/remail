import { describe, expect, it } from "vitest";

import {
  mergeOrderRuntimeState,
  shouldShowQuickFetchControl,
} from "./order-runtime";
import type { WorkbenchOrder } from "./types";

function order(overrides: Partial<WorkbenchOrder> = {}): WorkbenchOrder {
  return {
    afterSaleUntil: "2026-07-10T12:00:00Z",
    createdAt: "2026-07-10T10:00:00Z",
    deliveryEmail: "user@example.com",
    hasDelivery: false,
    id: "1",
    inventoryScope: "public_only",
    lastFetchedAt: "2026-07-10T10:00:00Z",
    messages: [],
    orderNo: "OR1",
    payAmount: 1,
    productId: "1",
    productType: "microsoft",
    projectId: "1",
    quantity: 1,
    serviceMode: "code",
    serviceState: "waiting_mail",
    status: "active",
    token: "",
    ...overrides,
  };
}

describe("mergeOrderRuntimeState", () => {
  it("preserves locally loaded mail and credentials during order refresh", () => {
    const current = order({
      hasDelivery: true,
      messages: [
        {
          body: "",
          id: "10",
          preview: "Code 123456",
          receivedAt: "2026-07-10T10:01:00Z",
          sender: "noreply@example.com",
          status: "matched",
          subject: "Code",
          verificationCode: "123456",
        },
      ],
      serviceState: "code_received",
      token: "st_token",
      verificationCode: "123456",
    });

    const merged = mergeOrderRuntimeState(order(), current);

    expect(merged.messages).toEqual(current.messages);
    expect(merged.hasDelivery).toBe(true);
    expect(merged.token).toBe("st_token");
    expect(merged.verificationCode).toBe("123456");
    expect(merged.serviceState).toBe("code_received");
  });
});

describe("shouldShowQuickFetchControl", () => {
  it("stops the purchase-order quick fetch control once a code is received", () => {
    const waiting = order({
      hasDelivery: true,
      serviceMode: "purchase",
    });

    expect(shouldShowQuickFetchControl(waiting)).toBe(true);
    expect(
      shouldShowQuickFetchControl({
        ...waiting,
        verificationCode: "123456",
      }),
    ).toBe(false);
  });

  it("does not show the quick fetch control for domain orders", () => {
    expect(
      shouldShowQuickFetchControl(
        order({
          productType: "domain",
          serviceMode: "purchase",
        }),
      ),
    ).toBe(false);
  });
});
