// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, render } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type {
  ProjectInventoryTotalResponse,
  ProjectProductSummary,
} from "@/lib/projects-api";

import {
  filterProducts,
  mergeProjectInventory,
  toWorkbenchProducts,
} from "../Dashboard";
import { ProductPickerPanel } from "./product-picker-panel";
import type { WorkbenchProject } from "./types";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) =>
      key === "Gmail email"
        ? "@gmail"
        : key === "Gmail variant"
          ? "谷歌变种"
          : key === "Gmail variant suffix"
            ? "变种@gmail"
            : key === "@outlook.com"
              ? "must not translate dynamic suffixes"
              : key,
  }),
}));

vi.mock("@/i18n/config", () => ({ default: { resolvedLanguage: "en" } }));

vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({ currentUser: undefined }),
}));

vi.mock("../apply-project-modal", () => ({ ApplyProjectModal: () => null }));
vi.mock("./mailbox-client", () => ({ MailboxClientModal: () => null }));
vi.mock("./order-panel", () => ({ OrderPanel: () => null }));
vi.mock("./project-list-panel", () => ({ ProjectListPanel: () => null }));

vi.mock("@douyinfe/semi-ui", () => ({
  Card: ({ children }: { children: ReactNode }) => children,
  Empty: () => null,
  Input: () => null,
  Tag: ({ children }: { children: ReactNode }) => children,
  Toast: { error: vi.fn() },
}));

vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));

vi.mock("@/components/semi/overflow-tooltip", () => ({
  OverflowTooltip: ({ children }: { children: ReactNode }) => children,
}));

afterEach(cleanup);

describe("ProductPickerPanel", () => {
  it("renders aggregate inventory on the parent and suffix inventory on the child", () => {
    const product: ProjectProductSummary = {
      activationWindowMinutes: 10,
      codeAvailable: 20,
      codeEnabled: true,
      codePrice: "1",
      codePublicAvailable: 16,
      codeWindowMinutes: 10,
      priceMultiplier: "1",
      publicAvailable: 18,
      purchaseAvailable: 19,
      purchaseEnabled: true,
      purchasePrice: "2",
      purchasePublicAvailable: 15,
      status: "enabled",
      suffixes: [
        { suffix: "outlook.com", totalAvailable: 7, publicAvailable: 5 },
      ],
      totalAvailable: 22,
      type: "microsoft",
      warrantyMinutes: 60,
    };
    const project = {
      products: toWorkbenchProducts(1, product),
    } as WorkbenchProject;
    const inventory: ProjectInventoryTotalResponse = {
      products: [
        {
          codeAvailable: 14,
          codePublicAvailable: 10,
          productType: "microsoft",
          publicAvailable: 11,
          purchaseAvailable: 12,
          purchasePublicAvailable: 8,
          suffixes: [
            { suffix: "outlook.com", totalAvailable: 3, publicAvailable: 2 },
          ],
          totalAvailable: 15,
        },
      ],
      projectId: 1,
      totalAvailable: 15,
    };
    const mergedProducts = mergeProjectInventory(project, inventory).products;
    const products = filterProducts(
      mergedProducts,
      "",
      "purchase",
      (key) => key,
    );
    const translate = vi.fn((key: string) => key);
    expect(
      filterProducts(mergedProducts, "outlook.com", "purchase", translate),
    ).toHaveLength(1);
    expect(translate).not.toHaveBeenCalledWith("@outlook.com");

    expect(products[0]).toMatchObject({
      emailSuffix: "outlook",
      id: "microsoft",
      purchaseInventory: 12,
      suffix: "Microsoft",
    });
    expect(products[1]).toMatchObject({
      emailSuffix: "outlook.com",
      id: "microsoft:outlook.com",
      purchaseInventory: 3,
      suffix: "@outlook.com",
    });
    expect(
      toWorkbenchProducts(1, { ...product, suffixes: [], type: "domain" })[0]
        .emailSuffix,
    ).toBe("domain");
    expect(
      toWorkbenchProducts(1, { ...product, suffixes: [], type: "gmail_variant" })[0],
    ).toMatchObject({
      emailSuffix: "gmail_variant",
      id: "gmail_variant",
      label: "Gmail variant",
      suffix: "Gmail variant suffix",
    });

    const noop = vi.fn();
    const { container } = render(
      <ProductPickerPanel
        inventoryScope="private_first"
        onInventoryScopeChange={noop}
        onProductSearchChange={noop}
        onSelectProduct={noop}
        onServiceModeChange={noop}
        priceMultiplier={1}
        productSearch=""
        products={products}
        selectedProductId="microsoft"
        serviceMode="purchase"
      />,
    );

    const rows = container.querySelectorAll(".workbench-product-row");
    expect(rows).toHaveLength(2);
    expect(rows[0]).not.toHaveClass("is-suffix");
    expect(rows[0]).toHaveTextContent("Stock 12");
    expect(rows[0]).not.toHaveTextContent("@outlook");
    expect(rows[1]).toHaveClass("is-suffix");
    expect(rows[1]).toHaveTextContent("@outlook.com");
    expect(rows[1]).toHaveTextContent("Stock 3");
  });

  it("groups the Gmail SKUs and filters their translated label", () => {
    const base: ProjectProductSummary = {
      activationWindowMinutes: 10,
      codeAvailable: 3,
      codeEnabled: true,
      codePrice: "1",
      codePublicAvailable: 3,
      codeWindowMinutes: 10,
      priceMultiplier: "1",
      publicAvailable: 3,
      purchaseAvailable: 3,
      purchaseEnabled: true,
      purchasePrice: "2",
      purchasePublicAvailable: 3,
      status: "enabled",
      suffixes: [],
      totalAvailable: 3,
      type: "gmail",
      warrantyMinutes: 60,
    };
    const products = [
      ...toWorkbenchProducts(1, { ...base, type: "gmail_variant" }),
      ...toWorkbenchProducts(1, { ...base, type: "icloud" }),
      ...toWorkbenchProducts(1, base),
    ];
    const translate = (key: string) =>
      key === "Gmail variant"
        ? "谷歌变种"
        : key === "Gmail variant suffix"
          ? "变种@gmail"
          : key;
    for (const search of ["谷歌变种", "变种@gmail"]) {
      expect(
        filterProducts(products, search, "purchase", translate).map(
          (product) => product.id,
        ),
      ).toEqual(["gmail_variant"]);
    }
    const noop = vi.fn();
    const { container } = render(
      <ProductPickerPanel
        inventoryScope="private_first"
        onInventoryScopeChange={noop}
        onProductSearchChange={noop}
        onSelectProduct={noop}
        onServiceModeChange={noop}
        priceMultiplier={1}
        productSearch=""
        products={products}
        selectedProductId="gmail"
        serviceMode="purchase"
      />,
    );

    expect(container.querySelector(".workbench-product-group")).toHaveTextContent("@gmail");
    const rows = container.querySelectorAll(".workbench-product-row");
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveClass("is-suffix");
    expect(rows[0]).toHaveTextContent("@gmail");
    expect(rows[1]).toHaveClass("is-suffix");
    expect(rows[1]).toHaveTextContent("谷歌变种");
    expect(rows[1]).toHaveTextContent("变种@gmail");
    expect(rows[2]).not.toHaveClass("is-suffix");
    expect(rows[2]).toHaveTextContent("iCloud");
  });
});
