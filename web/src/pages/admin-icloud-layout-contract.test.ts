// @ts-expect-error -- Vitest executes this source-contract test in Node; the
// browser application intentionally does not depend on Node type packages.
import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const appSource = readFileSync(new URL("../App.tsx", import.meta.url), "utf8");
const icloudSource = readFileSync(
  new URL("./AdminICloudEmails.tsx", import.meta.url),
  "utf8",
);
const microsoftSource = readFileSync(
  new URL("./AdminMicrosoftEmails.tsx", import.meta.url),
  "utf8",
);

describe("admin iCloud page layout", () => {
  it("keeps the Microsoft resource shell and routes to the iCloud API page", () => {
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
      "<Tabs.TabPane",
    ]) {
      expect(microsoftSource).toContain(fragment);
      expect(icloudSource).toContain(fragment);
    }

    for (const fragment of [
      "rowSelection",
      "useSelectionNotification",
      "listAdminICloudAliases",
      "listAdminICloudTasks",
      "includeFacets: false",
      "includeTotal: false",
      "batchAdminICloudResourcesByIds",
      "batchAdminICloudResourcesByFilter",
      "recoverAdminICloudResource",
      "deleteAdminICloudResource",
      "updateAdminICloudResource",
      "createAdminICloudImportPreparation",
      "getAdminICloudImportPreparation",
      "preparationId",
      "forwardToEmail",
      "<EditICloudModal",
      "<ICloudMaintenanceModal",
      "setMaintenanceTarget",
      't("Maintenance")',
      'label: "Create alias"',
      'accept=".txt,text/plain"',
      "await file.text()",
      "whitespace-pre-wrap break-all",
      '<Tabs.TabPane itemKey="aliases"',
      '<Tabs.TabPane itemKey="orders"',
      '<Tabs.TabPane itemKey="tasks"',
      '<Tabs.TabPane itemKey="mails"',
      'resourceType="icloud"',
      'dataIndex: "anonymousId"',
      'dataIndex: "newSession"',
      'dataIndex: "oldSession"',
      'dataIndex: "selectedForwardTo"',
      't("New Cookie")',
      't("Old Cookie")',
      't("Forwarding mailbox")',
    ]) {
      expect(icloudSource).toContain(fragment);
    }

    expect(icloudSource).not.toContain("onValidate");
    expect(icloudSource).not.toContain("overflow-x-auto");
    expect(icloudSource).not.toContain("gmailEmail");
    expect(icloudSource).not.toContain("----Gmail");
    expect(icloudSource).not.toContain('t("Linked Gmail")');
    expect(icloudSource).not.toContain("recipientMailId");
    expect(icloudSource).not.toContain("deliveryProbe");
    expect(icloudSource).not.toContain("appPassword");
    expect(icloudSource).not.toContain("IMAP health");
    expect(icloudSource).not.toContain("Last IMAP sync");

    expect(appSource).toContain(
      'adminICloudEmails: () => import("./pages/AdminICloudEmails")',
    );
    expect(appSource).toContain('path: "/admin/icloud"');
    expect(appSource).toContain("component: AdminICloudEmails");
    expect(icloudSource).toContain('from "@/lib/admin-icloud-api"');
    expect(icloudSource).not.toContain('from "@/lib/admin-microsoft-api"');
  });

  it("keeps account, family, and phone details out of the resource table", () => {
    const columnsStart = icloudSource.lastIndexOf("const columns = useMemo(");
    const columnsEnd = icloudSource.indexOf("const tableColumns =", columnsStart);
    const resourceColumns = icloudSource.slice(columnsStart, columnsEnd);

    expect(resourceColumns).not.toContain('title: t("Account role")');
    expect(resourceColumns).not.toContain('title: t("Family")');
    expect(resourceColumns).not.toContain('title: t("Bound phone")');
    expect(icloudSource).toContain('label={t("Account role")}');
    expect(icloudSource).toContain('label={t("Bound phone")}');
    expect(icloudSource).toContain('label={t("Family primary")}');
    expect(icloudSource).toContain('scroll={{ x: "max(100%, 2240px)"');
  });
});
