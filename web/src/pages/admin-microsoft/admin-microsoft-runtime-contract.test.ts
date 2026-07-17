// @ts-expect-error -- Vitest executes this source-contract test in Node; the
// browser application intentionally does not depend on Node type packages.
import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const appSource = readFileSync(new URL("../../App.tsx", import.meta.url), "utf8");
const pageSource = readFileSync(
  new URL("../AdminMicrosoftEmails.tsx", import.meta.url),
  "utf8"
);
const detailSource = readFileSync(
  new URL("./microsoft-detail-sheet.tsx", import.meta.url),
  "utf8"
);
const modalSource = readFileSync(
  new URL("./microsoft-modals.tsx", import.meta.url),
  "utf8"
);
const maintenanceSource = readFileSync(
  new URL("./microsoft-maintenance-modal.tsx", import.meta.url),
  "utf8"
);
const bulkMaintenanceSource = readFileSync(
  new URL("./microsoft-bulk-maintenance-modal.tsx", import.meta.url),
  "utf8"
);
const typeSource = readFileSync(
  new URL("./admin-microsoft-types.ts", import.meta.url),
  "utf8"
);

function expectContainsAll(source: string, values: readonly string[]) {
  for (const value of values) expect(source).toContain(value);
}

describe("admin Microsoft runtime UI contract", () => {
  it("routes /admin/microsoft to the real lazy-loaded page", () => {
    expectContainsAll(appSource, [
      'adminMicrosoftEmails: () => import("./pages/AdminMicrosoftEmails")',
      "const AdminMicrosoftEmails = lazy(pageLoaders.adminMicrosoftEmails);",
      'path: "/admin/microsoft"',
      "component: AdminMicrosoftEmails",
    ]);
    expect(appSource).not.toContain("function AdminMicrosoftEmails() {");
  });

  it("uses the real API adapter and generated OpenAPI types without mock imports", () => {
    const removedMockModule = ["admin", "microsoft", "mock"].join("-");
    for (const source of [pageSource, detailSource, modalSource, maintenanceSource, bulkMaintenanceSource]) {
      expect(source).toContain('from "@/lib/admin-microsoft-api"');
      expect(source).not.toContain(removedMockModule);
    }
    expect(typeSource).toContain(
      'import type { components, operations } from "@/lib/openapi/schema"'
    );
    expectContainsAll(typeSource, [
      'components["schemas"]["AdminMicrosoftResourceItem"]',
      'components["schemas"]["AdminMicrosoftResourceDetail"]',
      'components["schemas"]["AdminTaskView"]',
      'components["schemas"]["AdminMessageSummary"]',
      'components["schemas"]["AdminAllocationItem"]',
      'operations["getAdminMicrosoftResources"]',
    ]);
  });

  it("preserves list actions, filters, suffix tabs, and table columns", () => {
    expectContainsAll(pageSource, [
      't("Import")',
      't("Refresh")',
      't("Maintenance")',
      't("Maintain all")',
      't("Put on sale")',
      't("Convert to private")',
      't("Delete")',
      't("Filters")',
      't("Query")',
      't("Reset")',
      'const [activeSuffix, setActiveSuffix]',
      'const [searchKeyword, setSearchKeyword]',
      'const [createdAtRange, setCreatedAtRange]',
      'const [statusFilter, setStatusFilter]',
      'const [privateFilter, setPrivateFilter]',
      'const [longLivedFilter, setLongLivedFilter]',
      'const [graphFilter, setGraphFilter]',
      'const [tokenHealthFilter, setTokenHealthFilter]',
      '<Tabs.TabPane\n        itemKey="all"',
      'suffixCounts.map(([suffix, count])',
    ]);
    expectContainsAll(
      pageSource,
      [
        "suffix",
        "emailAddress",
        "bindingAddress",
        "ownerEmail",
        "status",
        "forSale",
        "longLived",
        "graphAvailable",
        "tokenHealth",
        "operate",
      ].map((column) => `dataIndex: "${column}"`)
    );
  });

  it("preserves detail tabs, task commands, modals, and credential safety copy", () => {
    expectContainsAll(detailSource, [
      '<Tabs.TabPane itemKey="basic" tab={t("Basic info")} />',
      '<Tabs.TabPane itemKey="orders" tab={t("Orders")} />',
      '<Tabs.TabPane itemKey="explicit" tab={t("Explicit aliases")} />',
      '<Tabs.TabPane itemKey="other" tab={t("Other aliases")} />',
      '<Tabs.TabPane itemKey="tasks" tab={t("Task details")} />',
      '<Tabs.TabPane itemKey="mails" tab={t("Mailbox")} />',
      '<Tabs.TabPane itemKey="auxiliary" tab={t("Auxiliary mailbox")} />',
      "validateAdminMicrosoftResource",
      "refreshAdminMicrosoftToken",
      "createAdminMicrosoftExplicitAlias",
      "fetchAdminMicrosoftMail",
      "scanAdminMicrosoftProjects",
      "listAdminMicrosoftTasks",
      "listAdminMicrosoftMessages",
      "getAdminMicrosoftMessage",
      "listAdminMicrosoftBindingMessages",
      "getAdminMicrosoftBindingMessage",
    ]);
    expectContainsAll(modalSource, [
      'title={t("Import Microsoft Emails")}',
      'title={t("Edit Microsoft resource")}',
      'title={t("Replace Microsoft credentials")}',
      "Credentials are accepted as write-only input. Passwords, client IDs and tokens are never returned by this page.",
      "Write-only. Leave blank to keep the current values; filling password replaces the whole credential set and re-queues validation.",
    ]);
    expectContainsAll(maintenanceSource, [
      'title={t("Microsoft resource maintenance")}',
      'label: "Validate resource"',
      'label: "Create alias"',
      'label: "Scan projects"',
      'label: "Update RT"',
    ]);
    expectContainsAll(bulkMaintenanceSource, [
      "maintainAdminMicrosoftResourcesByIds",
      "maintainAdminMicrosoftResourcesByFilter",
      'key: "validate"',
      'key: "alias"',
      'key: "history"',
      'key: "token"',
    ]);
    expectContainsAll(pageSource, [
      "setBulkMaintenanceTarget",
      'mode: "ids"',
      'mode: "filter"',
      'checkLabelKey: "Maintenance"',
      "<MicrosoftBulkMaintenanceModal",
    ]);
  });
});
