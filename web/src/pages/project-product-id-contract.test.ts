// @ts-expect-error -- Vitest runs this source contract in Node without Node types.
import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

const projectsSource = readFileSync(new URL("./Projects.tsx", import.meta.url), "utf8");
const workbenchSource = readFileSync(
  new URL("./workbench/product-picker-panel.tsx", import.meta.url),
  "utf8"
);
const styles = readFileSync(new URL("../index.css", import.meta.url), "utf8");

function cssRule(selector: string) {
  return styles.match(new RegExp(`\\.${selector}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
}

describe("project product IDs", () => {
  it("shows the API product ID in the project square and workbench", () => {
    expect(projectsSource).toContain("{productTypeLabel(product.type, t)}#{product.id}");
    expect(workbenchSource).toContain(
      "{productTypeLabel(product.productType, t)}#{product.productId}"
    );
  });

  it("wraps between project products without truncating their IDs", () => {
    expect(projectsSource).toContain('className="project-square-product-type"');
    expect(cssRule("project-square-product-types")).toContain("flex-wrap: wrap");
    expect(cssRule("project-square-product-types")).not.toMatch(
      /max-width|overflow:\s*hidden|text-overflow|white-space:\s*nowrap/
    );
    expect(cssRule("project-square-product-type")).toContain("white-space: nowrap");
  });
});
