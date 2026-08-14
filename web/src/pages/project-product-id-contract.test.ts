// @ts-expect-error -- Vitest runs this source contract in Node without Node types.
import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const projectsSource = readFileSync(new URL("./Projects.tsx", import.meta.url), "utf8");
const workbenchSource = readFileSync(
  new URL("./workbench/product-picker-panel.tsx", import.meta.url),
  "utf8"
);
const dashboardSource = readFileSync(new URL("./Dashboard.tsx", import.meta.url), "utf8");
const orderPanelSource = readFileSync(
  new URL("./workbench/order-panel.tsx", import.meta.url),
  "utf8"
);
const homeSource = readFileSync(new URL("./Home.tsx", import.meta.url), "utf8");
const publicOpenApiSource = readFileSync(
  new URL("../../public/openapi.json", import.meta.url),
  "utf8"
);
const styles = readFileSync(new URL("../index.css", import.meta.url), "utf8");

function cssRule(selector: string) {
  return styles.match(new RegExp(`\\.${selector}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
}

describe("internal project product IDs", () => {
  it("does not expose product IDs in project or workbench UI", () => {
    expect(projectsSource).not.toContain("product.id");
    expect(workbenchSource).not.toMatch(/\bproductId\b/);
  });

  it("submits suffix-based orders and keeps productId out of public examples", () => {
    expect(dashboardSource).toContain("emailSuffix: selectedProduct.emailSuffix");
    expect(dashboardSource).not.toMatch(/\bproductId\s*:/);
    expect(homeSource).toContain('{"projectId":1,"emailSuffix":"outlook.com"}');
    expect(homeSource).not.toMatch(/\bproductId\b/);
  });

  it("documents owned full-domain ordering in the public contract", () => {
    expect(publicOpenApiSource).toContain("mydomain.com");
    expect(publicOpenApiSource).not.toContain("其他受支持的公共后缀选择域名邮箱");
  });

  it("restores personalized inventory on focus and labels orders from their snapshot", () => {
    expect(dashboardSource).toMatch(
      /await loadWorkbenchProjects\(\);[\s\S]*loadProjectInventory\(selectedProjectIdRef\.current, \{[\s\S]*silent: true/
    );
    expect(orderPanelSource).toContain("productTypeLabel(order.productType, t)");
    expect(orderPanelSource).not.toContain("productsByKey");
  });

  it("wraps between project product types without truncating labels", () => {
    expect(projectsSource).toContain('className="project-square-product-type"');
    expect(cssRule("project-square-product-types")).toContain("flex-wrap: wrap");
    expect(cssRule("project-square-product-types")).not.toMatch(
      /max-width|overflow:\s*hidden|text-overflow|white-space:\s*nowrap/
    );
    expect(cssRule("project-square-product-type")).toContain("white-space: nowrap");
  });
});
