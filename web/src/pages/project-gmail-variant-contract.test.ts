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

describe("Gmail variant product contract", () => {
  it("keeps primary Gmail and its variant product separate", () => {
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

  it("documents variant checkout without exposing resource credentials", () => {
    const orderProperties = JSON.parse(publicOpenApiSource).components.schemas.Order.properties;
    expect(orderProperties.productType.enum).toContain("gmail_variant");
    for (const field of [
      "password", "twoFactorSecret", "appPassword", "refreshToken", "accessToken",
      "gmailPassword", "gmailTwoFactorSecret", "gmailAppPassword",
    ]) {
      expect(orderProperties).not.toHaveProperty(field);
      expect(orderDetailSource).not.toContain(`order.${field}`);
    }
    expect(orderDetailSource).toContain("order.serviceToken");
    expect(publicOpenApiSource).toContain("@googlemail.com 地址");
  });
});
