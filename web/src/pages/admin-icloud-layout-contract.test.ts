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
      "includeFacets: false",
      "includeTotal: false",
      "batchAdminICloudResourcesByIds",
      "batchAdminICloudResourcesByFilter",
      "recoverAdminICloudResource",
      "deleteAdminICloudResource",
      '<Tabs.TabPane itemKey="aliases"',
    ]) {
      expect(icloudSource).toContain(fragment);
    }

    expect(appSource).toContain(
      'adminICloudEmails: () => import("./pages/AdminICloudEmails")',
    );
    expect(appSource).toContain('path: "/admin/icloud"');
    expect(appSource).toContain("component: AdminICloudEmails");
    expect(icloudSource).toContain('from "@/lib/admin-icloud-api"');
    expect(icloudSource).not.toContain('from "@/lib/admin-microsoft-api"');
  });
});
