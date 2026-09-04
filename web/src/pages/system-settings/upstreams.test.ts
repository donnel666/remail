import { describe, expect, it } from "vitest";
// @ts-expect-error -- this source-contract check runs in Node without Node types.
import { readFileSync } from "node:fs";

const upstreamsSource = readFileSync(new URL("./upstreams.tsx", import.meta.url), "utf8");

describe("Kitesim upstream money commands", () => {
  it("prefills the script-compatible billing profile", () => {
    expect(upstreamsSource).toContain('firstName: "noreal"');
    expect(upstreamsSource).toContain('lastName: "name"');
    expect(upstreamsSource).toContain('phone: "6505438765"');
    expect(upstreamsSource).toContain('address: "1295 Charleston Rd"');
  });

  it("passes the selected purchase price as the command ceiling", () => {
    expect(upstreamsSource).toContain("selectedProduct.buyPrice");
    expect(upstreamsSource).toContain("purchaseKitesimNumbers(");
  });

  it("preserves an edited system account while background refreshes run", () => {
    expect(upstreamsSource).toContain("if (!accountEditedRef.current) setAccountId(next.accountId || 0);");
    expect(upstreamsSource).toContain("accountEditedRef.current = nextAccountId !== (upstream?.accountId ?? 0);");
    expect(upstreamsSource).toContain("accountEditedRef.current = false;");
  });
});
