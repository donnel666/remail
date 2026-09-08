// @ts-expect-error -- This source-contract test runs in Node, not the browser.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const protoSource = readFileSync(new URL("./AdminProtoEmails.tsx", import.meta.url), "utf8");
const microsoftSource = readFileSync(new URL("./AdminMicrosoftEmails.tsx", import.meta.url), "utf8");

function toolbar(source: string) {
  return source.slice(source.indexOf("  const actionsArea = ("), source.indexOf("  const paginationArea ="));
}

describe("admin Proto mailbox toolbar", () => {
  it("keeps the admin Microsoft layout and compact controls while retaining owner filtering", () => {
    const protoToolbar = toolbar(protoSource);
    const microsoftToolbar = toolbar(microsoftSource);
    for (const fragment of [
      'className="flex w-full flex-col items-center justify-between gap-2 md:flex-row"',
      'className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto"',
      'className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row"',
      'className="resources-search-input w-full md:w-56"',
      'placeholder={t("Search email, owner or ID")}',
      'style={{ width: isMobile ? "100%" : 224 }}',
      'style={{ width: isMobile ? "100%" : 380 }}',
      "<CompactModeToggle",
    ]) {
      expect(microsoftToolbar).toContain(fragment);
      expect(protoToolbar).toContain(fragment);
    }
    const ownerSelect = protoToolbar.match(/<AdminUserSelect[\s\S]*?\/>/)?.[0];
    expect(ownerSelect).toBeDefined();
    expect(ownerSelect).toContain('size="small"');
    expect(ownerSelect).toContain("value={ownerFilter}");
    expect(protoToolbar.replace(ownerSelect!, "").match(/size="small"/g)?.length).toBe(microsoftToolbar.match(/size="small"/g)?.length);
    expect(protoSource).toContain("filter.ownerId = ownerFilter");
    expect(protoSource).toContain("owners={importOwners}");
    expect(protoSource).not.toContain('from "@/lib/admin-microsoft-api"');
  });
});
