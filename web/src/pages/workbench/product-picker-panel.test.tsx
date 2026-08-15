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
  useTranslation: () => ({ t: (key: string) => key }),
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
    const products = filterProducts(
      mergeProjectInventory(project, inventory).products,
      "",
      "purchase",
    );

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
});
