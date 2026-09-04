// @ts-expect-error -- Vitest executes this source-contract test in Node; the
// browser application intentionally does not depend on Node type packages.
import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const adminSource = readFileSync(new URL("./AdminProjects.tsx", import.meta.url), "utf8");
const applicationSource = readFileSync(
  new URL("./apply-project-modal.tsx", import.meta.url),
  "utf8"
);
const projectsSource = readFileSync(new URL("./Projects.tsx", import.meta.url), "utf8");

describe("iCloud project products", () => {
  it("keeps iCloud available in project applications and admin product editors", () => {
    expect(applicationSource).toContain('<Select.Option value="icloud">');
    expect(adminSource).toContain('"microsoft", "domain", "gmail", "gmail_variant", "icloud"');
    expect(adminSource).toContain("purchaseEnabled: priceDefaults[type].purchaseEnabled");
    expect(adminSource).toContain('return type === "microsoft" || type === "icloud";');
    expect(projectsSource).toContain('types.add("icloud")');
  });
});
