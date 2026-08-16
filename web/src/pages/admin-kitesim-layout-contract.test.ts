// @ts-expect-error -- Vitest executes this source-contract test in Node; the
// browser application intentionally does not depend on Node type packages.
import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const appSource = readFileSync(new URL("../App.tsx", import.meta.url), "utf8");
const kitesimSource = readFileSync(
  new URL("./AdminKitesim.tsx", import.meta.url),
  "utf8",
);
const microsoftSource = readFileSync(
  new URL("./AdminMicrosoftEmails.tsx", import.meta.url),
  "utf8",
);

describe("admin Kitesim page layout", () => {
  it("keeps the Microsoft resource page shell and Kitesim inbox", () => {
    for (const fragment of [
      'className="console-content-width py-5"',
      'type="type3"',
      'className="flex w-full flex-col items-center justify-between gap-2 md:flex-row"',
      'className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto"',
      'className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row"',
      'className="resources-search-input w-full md:w-56"',
      'className="remail-toolbar-fixed-button flex-1 md:flex-none"',
      'className="overflow-hidden rounded-xl"',
      'scroll={{ x: "max(100%, 2030px)", y: DESKTOP_TABLE_SCROLL_Y }}',
      'size="middle"',
      "<StatisticFilterOption",
      "<CompactModeToggle",
      "rowSelection=",
      "useSelectionNotification({",
    ]) {
      expect(microsoftSource).toContain(fragment);
      expect(kitesimSource).toContain(fragment);
    }

    for (const fragment of [
      "<ImportKitesimModal",
      "<KitesimDetailSheet",
      "<KitesimMessagesPanel",
      "<KitesimRenewalModal",
      '<Tabs.TabPane itemKey="basic"',
      '<Tabs.TabPane itemKey="orders"',
      '<Tabs.TabPane itemKey="status"',
      '<Tabs.TabPane itemKey="renewal"',
      '<Tabs.TabPane itemKey="tasks"',
      '<Tabs.TabPane itemKey="mails"',
      '<Tabs.TabPane itemKey="account"',
      't("SMS inbox")',
      't("Renew")',
      "listAdminKitesimMessages",
      "listKitesimProducts",
      "renewKitesimPhone",
      "maxUnitPrice",
      'placeholder="account@example.com----password"',
    ]) {
      expect(kitesimSource).toContain(fragment);
    }

    expect(appSource).toContain(
      'adminKitesim: () => import("./pages/AdminKitesim")',
    );
    expect(appSource).toContain('path: "/admin/kitesim"');
    expect(appSource).toContain("component: AdminKitesim");
    expect(kitesimSource).toContain('from "@/lib/admin-kitesim-api"');
    expect(kitesimSource).not.toContain('from "@/lib/admin-microsoft-api"');
    expect(kitesimSource).toContain("const price = product.buyPrice;");
    expect(kitesimSource).toContain("const maxUnitPrice = selectedProduct.buyPrice;");
  });

  it("gates Kitesim mutations without changing the resource-page layout", () => {
    for (const fragment of [
      'permissionKey("core:resource", "write")',
      'permissionKey("core:resource", "operate")',
      'permissionKey("system:settings", "write")',
      "onCheck: canOperate ? syncSelected : undefined",
      'disabled={!canWrite}',
      'disabled={!canOperate || pagedItems.length === 0}',
      'item.status !== "active" && item.status !== "expired"',
      "item={canRenew ? renewalItem : null}",
      "canOperate={canOperate}",
    ]) {
      expect(kitesimSource).toContain(fragment);
    }
  });

  it("keeps every SMS entry visible but disables it without message permission", () => {
    for (const fragment of [
      'permissionKey("mailmatch:message", "read")',
      "canReadMessages={canReadMessages}",
      '<Tabs.TabPane itemKey="mails" disabled={!canReadMessages}',
      'activeTab === "mails" && canReadMessages',
      "disabled: !canReadMessages",
      "disabled={!canReadMessages || !item.phoneId || busy}",
      "disabled={!canReadMessages || selectedItems.length !== 1 || !selectedItems[0]?.phoneId}",
      "disabled={!canReadMessages || !item.phoneId}",
    ]) {
      expect(kitesimSource).toContain(fragment);
    }
  });
});
