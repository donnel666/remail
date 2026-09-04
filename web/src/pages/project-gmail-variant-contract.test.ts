// @ts-expect-error -- Vitest runs this source contract in Node without Node types.
import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const adminSource = readFileSync(new URL("./AdminProjects.tsx", import.meta.url), "utf8");
const applicationSource = readFileSync(
  new URL("./apply-project-modal.tsx", import.meta.url),
  "utf8",
);
const orderDetailSource = readFileSync(
  new URL("./orders/order-detail-modal.tsx", import.meta.url),
  "utf8",
);
const publicOpenApiSource = readFileSync(
  new URL("../../public/openapi.json", import.meta.url),
  "utf8",
);

describe("Gmail special product contract", () => {
  it("keeps primary Gmail and its dot-plus special product separate", () => {
    expect(adminSource).toContain('"gmail", "gmail_variant"');
    expect(adminSource).toMatch(/mainWeight:\s*product\.type === "gmail"\s*\? 1/);
    expect(adminSource).toContain('dotWeight: "0"');
    expect(adminSource).toMatch(
      /plusWeight:\s*product\.type === "gmail_variant"\s*\? 1/,
    );
    expect(adminSource).toContain('gmail_variant: readProduct("gmail_variant")');
    expect(adminSource).toContain("default_project_${type}");
    expect(applicationSource).toContain('<Select.Option value="gmail_variant">');
  });

  it("recognizes special purchases and documents the checkout selector", () => {
    expect(orderDetailSource).toContain('order.productType === "gmail_variant"');
    expect(publicOpenApiSource).toContain("gmail_variant");
    expect(publicOpenApiSource).toContain("点号别名或加号别名");
  });
});
