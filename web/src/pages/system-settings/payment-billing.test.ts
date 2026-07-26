import { describe, expect, it } from "vitest";

import { applyEPayURLDefaults, changeEPayVersion } from "./payment-billing-keys";
import { parseTopupTiers, serializeTopupTiers } from "./topup-tiers";

describe("topup tier settings", () => {
  it("converts between visible tier rows and the existing JSON settings", () => {
    const tiers = parseTopupTiers("[10, 100]", '{"100":5,"200":15}');
    expect(tiers).toEqual([{ amount: 10, bonus: 0 }, { amount: 100, bonus: 5 }, { amount: 200, bonus: 15 }]);
    expect(serializeTopupTiers(tiers)).toEqual({
      topup_amount_presets: "[10,100,200]",
      topup_amount_bonus: '{"100":5,"200":15}',
    });
  });
});

describe("EPay callback settings", () => {
  it("fills the current origin and follows the selected protocol version", () => {
    const initial = applyEPayURLDefaults({ epay_version: "v1", epay_notify_url: "", epay_return_url: "" }, "https://app.example.com");
    expect(initial.epay_notify_url).toBe("https://app.example.com/v1/payments/webhooks/epay/v1");
    expect(initial.epay_return_url).toBe("https://app.example.com/payment/return");
    expect(changeEPayVersion(initial, "v2", "https://app.example.com").epay_notify_url).toBe("https://app.example.com/v1/payments/webhooks/epay/v2");
  });

  it("preserves an explicitly configured callback URL", () => {
    const form = { epay_version: "v1", epay_notify_url: "https://callback.example.com/notify", epay_return_url: "https://app.example.com/wallet" };
    expect(changeEPayVersion(form, "v2", "https://app.example.com").epay_notify_url).toBe(form.epay_notify_url);
  });
});
