import { describe, expect, it } from "vitest";

import {
  mergeOrderRuntimeState,
  shouldAutoFetchOrderMail,
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

  it("uses the server delivery code instead of a locally selected later message", () => {
    const current = order({
      hasDelivery: true,
      verificationCode: "970183",
    });
    const merged = mergeOrderRuntimeState(
      order({ hasDelivery: true, verificationCode: "344992" }),
      current,
    );

    expect(merged.verificationCode).toBe("344992");
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

  it("shows the quick fetch control for domain orders", () => {
    expect(
      shouldShowQuickFetchControl(
        order({
          productType: "domain",
          serviceMode: "purchase",
        }),
      ),
    ).toBe(true);
  });
});

describe("shouldAutoFetchOrderMail", () => {
  const now = Date.parse("2026-07-10T11:00:00Z");

  it.each([
    ["pending activation", "pending_activation"],
    ["in warranty", "in_warranty"],
  ] as const)(
    "allows automatic fetch for a recent purchase order that is %s",
    (_, serviceState) => {
      expect(
        shouldAutoFetchOrderMail(
          order({
            createdAt: "2026-07-10T10:30:00Z",
            serviceMode: "purchase",
            serviceState,
          }),
          now,
        ),
      ).toBe(true);
    },
  );

  it("allows automatic fetch until the one-hour window expires", () => {
    expect(
      shouldAutoFetchOrderMail(
        order({
          createdAt: "2026-07-10T10:01:00Z",
          serviceMode: "purchase",
          serviceState: "in_warranty",
        }),
        now,
      ),
    ).toBe(true);
  });

  it("stops automatic fetch after one hour", () => {
    expect(
      shouldAutoFetchOrderMail(
        order({
          createdAt: "2026-07-10T10:00:00Z",
          serviceMode: "purchase",
          serviceState: "in_warranty",
        }),
        now,
      ),
    ).toBe(false);
  });

  it.each(["warranty_ended", "activation_timeout"] as const)(
    "stops automatic fetch for purchase orders that are %s",
    (serviceState) => {
      expect(
        shouldAutoFetchOrderMail(
          order({
            createdAt: "2026-07-10T10:30:00Z",
            serviceMode: "purchase",
            serviceState,
          }),
          now,
        ),
      ).toBe(false);
    },
  );

  it("stops automatic fetch when the creation time is invalid", () => {
    expect(
      shouldAutoFetchOrderMail(
        order({
          createdAt: "invalid",
          serviceMode: "purchase",
          serviceState: "pending_activation",
        }),
        now,
      ),
    ).toBe(false);
  });

  it("keeps automatic fetch enabled for code orders", () => {
    expect(shouldAutoFetchOrderMail(order(), now)).toBe(true);
  });
});
