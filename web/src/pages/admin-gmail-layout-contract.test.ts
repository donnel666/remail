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
    expect(gmailSource).not.toContain('type="type1"');
    expect(gmailSource).not.toContain("descriptionArea=");
  });
});
