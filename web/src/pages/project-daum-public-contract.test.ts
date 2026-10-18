// @ts-expect-error -- Vitest runs this public contract in Node without Node types.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { DAUM_EMAIL_SUFFIXES } from "./resources/daum-model";

const spec = JSON.parse(readFileSync(new URL("../../public/openapi.json", import.meta.url), "utf8"));
const schemas = spec.components.schemas;

describe("Daum public checkout contract", () => {
  it("exposes Daum product and allocation enums with stable SDK names and project facets", () => {
    const productFilter = spec.paths["/v1/open/projects"].get.parameters.find((parameter: { name: string }) => parameter.name === "productType");
    for (const schema of [schemas.ProjectProductSummary.properties.type, schemas.Order.properties.productType, schemas.Order.properties.allocationType, productFilter.schema]) {
      const index = schema.enum.indexOf("daum");
      expect(index).toBeGreaterThanOrEqual(0);
      expect(schema["x-enum-varnames"][index]).toBe("Daum");
    }
    expect(schemas.ProjectFacets.properties.productType.properties.daum).toMatchObject({ type: "integer", minimum: 0 });
  });

  it("documents exact suffixes, inventory-weighted random choice and fixed batch suffixes", () => {
    for (const name of ["CreateOrderRequest", "CreateOrderBatchRequest"]) {
      const description = schemas[name].properties.emailSuffix.description;
      for (const suffix of DAUM_EMAIL_SUFFIXES) expect(description).toContain(suffix);
      expect(description).toContain("daum 在有货的 Daum 后缀中按库存量加权随机");
      expect(description).toContain("指定具体后缀缺货时不会切换其他后缀");
    }
    expect(schemas.CreateOrderBatchRequest.properties.emailSuffix.description).toContain("批量下单只随机一次");
    expect(schemas.ProductSuffixInventory.properties.suffix.description).toContain("currently available stock for that suffix");
    for (const suffix of DAUM_EMAIL_SUFFIXES) expect(schemas.ProductSuffixInventory.properties.suffix.description).toContain(suffix);
  });

  it.each(["/v1/open/orders", "/v1/open/orders/batch"])("provides working selectors for every Daum choice at %s", (path) => {
    const examples = Object.values(spec.paths[path].post.requestBody.content["application/json"].examples) as Array<{ value: { projectId: number; emailSuffix: string; quantity?: number } }>;
    expect(examples.map((example) => example.value.emailSuffix)).toEqual(["daum", ...DAUM_EMAIL_SUFFIXES]);
    for (const { value } of examples) {
      expect(value.projectId).toBeGreaterThan(0);
      expect(value).not.toHaveProperty("productId");
      if (path.endsWith("/batch")) expect(value.quantity).toBe(2);
    }
  });

  it("keeps order pickup scoped to the exact address without exposing imported credentials or user resource APIs", () => {
    const pickupEmail = schemas.PickupCredentialRequest.properties.email;
    for (const suffix of DAUM_EMAIL_SUFFIXES) expect(pickupEmail.description).toContain(suffix);
    expect(pickupEmail.example).toBe("account@hanmail2.net");
    for (const path of ["/v1/pickup", "/v1/pickup/messages/{messageId}"]) {
      const parameters = spec.paths[path].get.parameters;
      expect(parameters.find((parameter: { name: string }) => parameter.name === "email").description).toBe(pickupEmail.description);
      expect(parameters.find((parameter: { name: string }) => parameter.name === "token").description).toContain("serviceToken");
    }
    for (const name of ["password", "appPassword", "pickupUrl", "bindingAddress", "kakaoEmail"]) expect(schemas.Order.properties).not.toHaveProperty(name);
    expect(schemas.Order.properties.serviceToken.description).toContain("不交付导入的 remail 取件 URL");
    expect(Object.keys(spec.paths).some((path) => path.includes("/daum/resources"))).toBe(false);
  });

  it("describes Daum purchase choices without disclosing account relationships", () => {
    const descriptions = [
      schemas.PickupCredentialRequest.properties.email.description,
      schemas.ProductSuffixInventory.properties.suffix.description,
      ...Object.values(spec.paths["/v1/open/orders"].post.requestBody.content["application/json"].examples).map((example: any) => example.summary),
    ].join(" ");
    expect(descriptions).not.toMatch(/显式别名|共用收件箱|主地址|alias|shared inbox|account count/i);
    expect(schemas.PickupCredentialRequest.properties.email.description).toContain("取件必须使用订单交付的原始地址");
  });

  it.each(["en", "zh"])("keeps Daum account relationships out of the public %s language payload", (locale) => {
    const labels = JSON.parse(readFileSync(new URL(`../i18n/locales/${locale}.json`, import.meta.url), "utf8")) as Record<string, string>;
    const disclosedRelationships = Object.entries(labels)
      .filter(([key, value]) => /daum/i.test(`${key} ${value}`))
      .filter(([key, value]) => /fixed aliases|share.{0,20}inbox|固定别名|共用收件箱/i.test(`${key} ${value}`))
      .map(([key]) => key);
    expect(disclosedRelationships).toEqual([]);
  });
});
