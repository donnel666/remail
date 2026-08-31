import { describe, expect, it } from "vitest";

import { formatPickupURLExport } from "./order-pickup-export";

describe("formatPickupURLExport", () => {
  it("writes email and pickup URL one per line", () => {
    expect(
      formatPickupURLExport([
        { email: "one@example.com", url: "https://example.com/pickup?token=1" },
        { email: "two@example.com", url: "https://example.com/pickup?token=2" },
      ])
    ).toBe(
      "one@example.com----https://example.com/pickup?token=1\n" +
        "two@example.com----https://example.com/pickup?token=2"
    );
  });
});
