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
      "<KitesimTaskDiagnostics",
      "<ServerPaginatedDrawerTable",
      "<KitesimRenewalModal",
      '<Tabs.TabPane itemKey="basic"',
      '<Tabs.TabPane itemKey="tasks"',
      '<Tabs.TabPane itemKey="mails"',
      't("Inbox")',
      't("Renew")',
      "listAdminKitesimMessages",
      "listAdminKitesimAccountTasks",
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
    expect(microsoftSource).toContain('scroll={{ x: "max(100%, 2030px)", y: DESKTOP_TABLE_SCROLL_Y }}');
    expect(kitesimSource).toContain('scroll={{ x: "max(100%, 1960px)", y: DESKTOP_TABLE_SCROLL_Y }}');
    expect(kitesimSource).not.toContain('dataIndex: "orderNo"');
    expect(kitesimSource).not.toContain("window.setInterval(() => void refreshList()");
    expect(kitesimSource).not.toContain('<Tabs.TabPane itemKey="orders"');
    expect(kitesimSource).not.toContain('<Tabs.TabPane itemKey="status"');
    expect(kitesimSource).not.toContain('<Tabs.TabPane itemKey="renewal"');
    expect(kitesimSource).not.toContain('<Tabs.TabPane itemKey="account"');
    expect(kitesimSource).toContain("globalThis.setTimeout");
    expect(kitesimSource).not.toContain('t("Synchronize selected")');
    expect(kitesimSource).not.toContain('t("Clear selection")');
  });

  it("gates Kitesim mutations without changing the resource-page layout", () => {
    for (const fragment of [
      'permissionKey("core:resource", "write")',
      'permissionKey("core:resource", "operate")',
      'permissionKey("system:settings", "write")',
      "onDelete: canOperate ? confirmDeleteSelected : undefined",
      "onSell: canOperate && phoneIDs(selectedItems).length > 0 ? confirmDisableSelected : undefined",
      "disableAdminKitesimPhones",
      "enableAdminKitesimPhones",
      "deleteAdminKitesimPhones",
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
      "disabled={!canReadMessages || !item.phoneId || busy}",
      "disabled={!canReadMessages || !item.phoneId}",
    ]) {
      expect(kitesimSource).toContain(fragment);
    }
  });

  it("copies the local phone number without its dialing code", () => {
    expect(kitesimSource).toContain('return value.replace(/^\\+\\d+\\s+/, "");');
    expect(kitesimSource).toContain("copyContent={phoneCopyContent(item.phoneNumber)}");
    expect(kitesimSource).toContain("copy(phoneCopyContent(item.phoneNumber))");
  });
});
