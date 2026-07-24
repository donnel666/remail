import { describe, expect, it } from "vitest";

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
