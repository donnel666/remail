import { describe, expect, it } from "vitest";

import {
  compareDomainSuffixes,
  compareProjectNames,
  normalizeInventorySuffix,
  pickupMessagesToWorkbench,
  productTypeLabel,
} from "./utils";

describe("compareProjectNames", () => {
  it("places names containing Chinese characters after non-Chinese names", () => {
    expect(compareProjectNames("中文项目", "Zulu")).toBeGreaterThan(0);
    expect(compareProjectNames("Mail 中文", "Zulu")).toBeGreaterThan(0);
    expect(compareProjectNames("Alpha", "Beta")).toBeLessThan(0);
  });
});

describe("domain suffix selections", () => {
  it("sorts displayed public suffixes", () => {
    const values = ["@net", "@com.cn", "@com"];
    expect(values.sort(compareDomainSuffixes)).toEqual([
      "@com",
      "@com.cn",
      "@net",
    ]);
  });

  it("keeps private domains and rejects full mailboxes", () => {
    expect(normalizeInventorySuffix("mydomain.com")).toBe("mydomain.com");
    expect(normalizeInventorySuffix("@com.cn")).toBe("com.cn");
    expect(normalizeInventorySuffix("alice@mydomain.com")).toBe("");
  });
});

describe("Gmail variant label", () => {
  it("keeps the plus-only product distinct from Gmail", () => {
    expect(productTypeLabel("gmail_variant", (key) => key)).toBe("Gmail variant");
  });
});

describe("pickup message normalization", () => {
  it("keeps stored IDs and gives id-less upstream items unique local keys", () => {
    const messages = pickupMessagesToWorkbench([
      {
        bodyPreview: "",
        receivedAt: "2026-08-31T08:00:00Z",
        recipient: "buyer@gmail.com",
        sender: "",
        subject: "",
        verificationCode: "111111",
      },
      {
        bodyPreview: "",
        receivedAt: "2026-08-31T08:01:00Z",
        recipient: "buyer@gmail.com",
        sender: "",
        subject: "",
        verificationCode: "222222",
      },
      {
        bodyPreview: "stored",
        id: 42,
        receivedAt: "2026-08-31T08:02:00Z",
        recipient: "buyer@gmail.com",
        sender: "sender@example.com",
        subject: "Code",
      },
    ]);

    expect(messages.map((message) => message.id)).toEqual([
      "pickup-0-2026-08-31T08:00:00Z",
      "pickup-1-2026-08-31T08:01:00Z",
      "42",
    ]);
    expect(new Set(messages.map((message) => message.id)).size).toBe(3);
  });
});
