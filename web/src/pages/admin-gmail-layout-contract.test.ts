// @ts-expect-error -- Vitest executes this source-contract test in Node; the
// browser application intentionally does not depend on Node type packages.
import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const gmailSource = readFileSync(
  new URL("./AdminGmailEmails.tsx", import.meta.url),
  "utf8"
);
const microsoftSource = readFileSync(
  new URL("./AdminMicrosoftEmails.tsx", import.meta.url),
  "utf8"
);
const icloudSource = readFileSync(
  new URL("./AdminICloudEmails.tsx", import.meta.url),
  "utf8"
);

describe("admin Gmail page layout", () => {
  it("reuses the Microsoft resource page shell", () => {
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
    ]) {
      expect(microsoftSource).toContain(fragment);
      expect(gmailSource).toContain(fragment);
    }

    expect(gmailSource).toContain("<StatisticFilterOption");
    expect(gmailSource).toContain("<CompactModeToggle");
    for (const fragment of [
      "rowSelection=",
      "useSelectionNotification({",
      't("Details")',
      't("Edit")',
      't("Maintenance")',
      't("Delete")',
      't("Recover")',
      "openBulkMaintenance(true)",
      'confirmBatch("publish", true)',
      'confirmBatch("unpublish", true)',
      'confirmBatch("delete", true)',
      "<GmailDetailSheet",
      "<EditGmailModal",
      "<GmailMaintenanceModal",
      "<RelatedOrdersTable",
      "<GmailTaskDiagnostics",
      "<ResourceMailsPanel",
      'tab={t("Validation")}',
      'tab={t("Other aliases")}',
      'tab={t("Orders")}',
      'tab={t("Task details")}',
      'tab={t("Mailbox")}',
      'resourceType="gmail"',
      "batchAllMatchingAdminGmailResources",
      'activeTab === "validation"',
      'activeTab === "other"',
    ]) {
      expect(gmailSource).toContain(fragment);
    }
    const basicTabBody = gmailSource.match(
      /\{activeTab === "basic" \? \(([\s\S]*?)\) : null\}/,
    )?.[1];
    expect(basicTabBody).toBeDefined();
    expect(basicTabBody).not.toContain("ConfiguredTag");
    expect(gmailSource).not.toContain("<ReplaceGmailCredentialsModal");
    expect(gmailSource).not.toContain('t("Replace credentials")');
    expect(gmailSource).toContain(
      "target={canWrite || canOperate ? editTarget : null}",
    );
    expect(gmailSource).toContain("await replaceAdminGmailCredentials");
    expect(gmailSource).toContain("width: 360");
    for (const source of [gmailSource, icloudSource]) {
      expect(source).toContain('width={isMobile ? "100%" : 940}');
    }
    expect(gmailSource).not.toContain("ADMIN_GMAIL_BATCH_MAX");
    expect(gmailSource).not.toContain("request.credentials");
    expect(gmailSource).not.toContain('type="type1"');
    expect(gmailSource).not.toContain("descriptionArea=");
  });
});

describe("admin resource import ownership wiring", () => {
  it("uses the current-user-first owner list on every import modal", () => {
    for (const source of [microsoftSource, gmailSource, icloudSource]) {
      expect(source).toContain(
        "ownersWithCurrentUserFirst(owners, currentUser)"
      );
      expect(source).toContain("owners={importOwners}");
    }
  });
});
