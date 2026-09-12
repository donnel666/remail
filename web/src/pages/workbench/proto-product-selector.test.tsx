// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ProjectInventoryTotalResponse, ProjectItem, ProjectProductSummary } from "@/lib/projects-api";
import Dashboard, { filterProducts, mergeProjectInventory, toWorkbenchProducts } from "../Dashboard";
import { ProductPickerPanel } from "./product-picker-panel";
import type { InventoryScope, ServiceMode, WorkbenchProduct, WorkbenchProject } from "./types";

const mocks = vi.hoisted(() => ({
  createOrder: vi.fn(), createOrderBatch: vi.fn(), listOrders: vi.fn(), getOrder: vi.fn(),
  listProjects: vi.fn(), inventory: vi.fn(), toastError: vi.fn(), toastSuccess: vi.fn(),
  translate: (key: string) => key,
}));

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@/i18n/config", () => ({ default: { resolvedLanguage: "en" } }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: undefined }) }));
vi.mock("@/lib/projects-api", () => ({ listProjects: mocks.listProjects, getProjectInventory: mocks.inventory }));
vi.mock("@/lib/orders-api", () => ({
  createOrder: mocks.createOrder, createOrderBatch: mocks.createOrderBatch,
  listOrders: mocks.listOrders, getOrder: mocks.getOrder,
}));
vi.mock("@/lib/mailmatch-api", () => ({
  readPickupMail: vi.fn(), readPickupMailBatch: vi.fn(), readPickupMessage: vi.fn(),
}));
vi.mock("../apply-project-modal", () => ({ ApplyProjectModal: () => null }));
vi.mock("./mailbox-client", () => ({ MailboxClientModal: () => null }));
vi.mock("./project-list-panel", () => ({ ProjectListPanel: () => null }));
vi.mock("./order-panel", () => ({
  OrderPanel: ({ creating, onCreateOrder, onQuantityChange, selectedProduct, selectedProductInventory }: {
    creating: boolean; onCreateOrder: () => void; onQuantityChange: (value: number) => void;
    selectedProduct?: WorkbenchProduct; selectedProductInventory: number;
  }) => <>
    <output data-testid="selected-selector">{selectedProduct?.emailSuffix}</output>
    <button onClick={() => onQuantityChange(2)}>Two orders</button>
    <button disabled={creating || !selectedProduct || selectedProductInventory <= 0} onClick={onCreateOrder}>Create fixture order</button>
  </>,
}));
vi.mock("@douyinfe/semi-ui", () => ({
  Card: ({ children }: { children: ReactNode }) => children,
  Empty: () => null,
  Input: ({ placeholder, onChange, value }: { placeholder: string; onChange: (value: string) => void; value: string }) =>
    <input aria-label={placeholder} onChange={(event) => onChange(event.target.value)} value={value} />,
  Tag: ({ children }: { children: ReactNode }) => children,
  Toast: { error: mocks.toastError, success: mocks.toastSuccess },
}));
vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("@/components/semi/overflow-tooltip", () => ({
  OverflowTooltip: ({ children, className }: { children: ReactNode; className?: string }) => <span className={className}>{children}</span>,
}));

const product: ProjectProductSummary = {
  activationWindowMinutes: 10, codeAvailable: 12, codePublicAvailable: 7, codeEnabled: true,
  codePrice: "1.25", codeWindowMinutes: 5, priceMultiplier: "1", publicAvailable: 7,
  purchaseAvailable: 12, purchasePublicAvailable: 7, purchaseEnabled: true, purchasePrice: "2.5",
  status: "enabled", totalAvailable: 12, type: "proto", warrantyMinutes: 60,
  suffixes: [
    { suffix: "protonmail.com", totalAvailable: 7, publicAvailable: 4 },
    { suffix: "proton.me", totalAvailable: 5, publicAvailable: 3 },
  ],
};
const inventory: ProjectInventoryTotalResponse = {
  projectId: 1, totalAvailable: 12,
  products: [{ productType: "proto", totalAvailable: 12, publicAvailable: 7,
    codeAvailable: 12, codePublicAvailable: 7, purchaseAvailable: 12, purchasePublicAvailable: 7,
    suffixes: product.suffixes }],
};
const project: ProjectItem = {
  id: 1, name: "Proto fixture", targetPlatform: "", status: "listed", accessType: "public",
  looseMatch: false, productCount: 1, mailRuleCount: 1, supportsDotAlias: false, supportsPlusAlias: false,
  products: [product], createdAt: "2026-09-12T00:00:00Z", updatedAt: "2026-09-12T00:00:00Z",
};

beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
  vi.stubGlobal("fetch", vi.fn(() => { throw new Error("Unexpected network request"); }));
  mocks.listProjects.mockResolvedValue({ items: [project] });
  mocks.inventory.mockResolvedValue(inventory);
  mocks.listOrders.mockResolvedValue({ items: [], hasNext: false });
  mocks.createOrder.mockImplementation(async (_payload, options) => ({
    id: 1, orderNo: "fixture-order", projectId: 1, productType: "proto", serviceMode: options.serviceMode,
    supplyPolicy: options.supply, status: "paid", payAmount: "1", deliveryEmail: "fixture@proton.me",
    createdAt: "2026-09-12T00:00:00Z", updatedAt: "2026-09-12T00:00:00Z",
  }));
  mocks.createOrderBatch.mockResolvedValue([]);
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function productRows(container: HTMLElement) {
  return Array.from(container.querySelectorAll<HTMLButtonElement>(".workbench-product-row"));
}

describe("Proto product hierarchy", () => {
  it("keeps aggregate inventory on the parent and distinct exact selectors on the children", () => {
    const products = toWorkbenchProducts(1, product);
    expect(products.map((item) => item.id)).toEqual(["proto", "proto:protonmail.com", "proto:proton.me"]);
    expect(products[0]).toMatchObject({ label: "Proto", emailSuffix: "proto", suffix: "Proto", codeInventory: 12, purchaseInventory: 12 });
    expect(products[1]).toMatchObject({ emailSuffix: "protonmail.com", suffix: "@protonmail.com", codeInventory: 7, codePublicInventory: 4, purchaseInventory: 7, purchasePublicInventory: 4 });
    expect(products[2]).toMatchObject({ emailSuffix: "proton.me", suffix: "@proton.me", codeInventory: 5, codePublicInventory: 3, purchaseInventory: 5, purchasePublicInventory: 3 });
    for (const item of products) {
      expect(item).toMatchObject({ codePrice: 1.25, purchasePrice: 2.5, priceMultiplier: 1 });
    }
  });

  it("shows known suffixes at zero when details are absent without fabricating stock or aliases", () => {
    const products = toWorkbenchProducts(1, { ...product, suffixes: undefined });
    expect(products[0].purchaseInventory).toBe(12);
    expect(products.slice(1).map((item) => [item.emailSuffix, item.codeInventory, item.purchasePublicInventory])).toEqual([
      ["protonmail.com", 0, 0], ["proton.me", 0, 0],
    ]);
    const filtered = toWorkbenchProducts(1, { ...product, suffixes: [
      { suffix: "proto.me", totalAvailable: 99, publicAvailable: 99 },
      { suffix: "@proton.me", totalAvailable: 2, publicAvailable: 1 },
      { suffix: "proton.me", totalAvailable: 2, publicAvailable: 1 },
    ] });
    expect(filtered.map((item) => item.emailSuffix)).toEqual(["proto", "protonmail.com", "proton.me"]);
    expect(filtered[2].purchaseInventory).toBe(2);
  });

  it("updates suffix stock without losing children, duplicating parents or merging selectors", () => {
    const initial = { products: toWorkbenchProducts(1, product) } as WorkbenchProject;
    const next: ProjectInventoryTotalResponse = { projectId: 1, totalAvailable: 3, products: [{
      productType: "proto", totalAvailable: 3, publicAvailable: 1,
      suffixes: [{ suffix: "proton.me", totalAvailable: 3, publicAvailable: 1 }],
    }] };
    const merged = mergeProjectInventory(mergeProjectInventory(initial, next), next);
    expect(merged.products.map((item) => item.id)).toEqual(["proto", "proto:protonmail.com", "proto:proton.me"]);
    expect(merged.products.map((item) => item.purchaseInventory)).toEqual([3, 0, 3]);
    expect(merged.products.map((item) => item.purchasePublicInventory)).toEqual([1, 0, 1]);
    expect(mergeProjectInventory(merged, { projectId: 1, totalAvailable: 0, products: [] }).products).toEqual(merged.products);
    expect(filterProducts(merged.products, "Proto", "purchase", mocks.translate)).toHaveLength(3);
    expect(filterProducts(merged.products, "@proton.me", "code", mocks.translate).map((item) => item.id)).toEqual(["proto:proton.me"]);
    expect(filterProducts(merged.products, "protonmail.com", "purchase", mocks.translate).map((item) => item.id)).toEqual(["proto:protonmail.com"]);
    expect(filterProducts(toWorkbenchProducts(1, { ...product, codeEnabled: false }), "", "code", mocks.translate)).toEqual([]);
  });

  it.each(["code", "purchase"] as ServiceMode[])("renders scoped %s stock and price with the existing child-row styling", (serviceMode) => {
    const products = toWorkbenchProducts(1, product);
    const noop = vi.fn();
    for (const scope of ["private_first", "public_only"] as InventoryScope[]) {
      const view = render(<ProductPickerPanel inventoryScope={scope} onInventoryScopeChange={noop}
        onProductSearchChange={noop} onSelectProduct={noop} onServiceModeChange={noop} priceMultiplier={1}
        productSearch="" products={products} selectedProductId="proto" serviceMode={serviceMode} />);
      const rows = productRows(view.container);
      expect(rows).toHaveLength(3);
      expect(rows[0]).not.toHaveClass("is-suffix");
      expect(rows[1]).toHaveClass("is-suffix");
      expect(rows[2]).toHaveClass("is-suffix");
      const expectedStock = scope === "public_only" ? [7, 4, 3] : [12, 7, 5];
      rows.forEach((row, index) => {
        expect(row).toHaveTextContent("Stock " + expectedStock[index]);
        expect(row.querySelector(".workbench-product-prices")).toHaveAttribute("aria-label", serviceMode === "code" ? "1.25" : "2.5");
      });
      view.unmount();
    }
  });

  it.each(["purchase", "code"] as ServiceMode[])("submits parent and child selectors through the actual %s checkout handler", async (serviceMode) => {
    const { container } = render(<Dashboard />);
    await waitFor(() => expect(productRows(container)).toHaveLength(3));
    if (serviceMode === "code") fireEvent.click(screen.getByRole("button", { name: "Code receiving" }));
    for (const selector of ["proto", "proton.me", "protonmail.com"]) {
      const suffix = selector === "proto" ? "Proto" : "@" + selector;
      const row = productRows(container).find((item) => item.querySelector(".workbench-product-suffix")?.textContent === suffix)!;
      fireEvent.click(row);
      expect(screen.getByTestId("selected-selector")).toHaveTextContent(selector);
      const create = screen.getByRole("button", { name: "Create fixture order" });
      fireEvent.click(create);
      await waitFor(() => expect(mocks.createOrder).toHaveBeenLastCalledWith(
        { projectId: 1, emailSuffix: selector }, expect.objectContaining({ serviceMode, supply: "private_first" }),
      ));
      await waitFor(() => expect(create).toBeEnabled());
    }
    expect(mocks.createOrder.mock.calls.map(([payload]) => payload.emailSuffix)).toEqual(["proto", "proton.me", "protonmail.com"]);
    fireEvent.change(screen.getByRole("textbox", { name: "Search suffix" }), { target: { value: "protonmail.com" } });
    expect(productRows(container)).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Two orders" }));
    fireEvent.click(screen.getByRole("button", { name: "Create fixture order" }));
    await waitFor(() => expect(mocks.createOrderBatch).toHaveBeenCalledWith(
      { projectId: 1, emailSuffix: "protonmail.com", quantity: 2 }, expect.objectContaining({ serviceMode }),
    ));
    await act(async () => undefined);
    expect(screen.getByTestId("selected-selector")).toHaveTextContent("protonmail.com");
    fireEvent.change(screen.getByRole("textbox", { name: "Search suffix" }), { target: { value: "" } });
    expect(productRows(container)).toHaveLength(3);
    expect(screen.getByTestId("selected-selector")).toHaveTextContent("protonmail.com");
    expect(mocks.toastError).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("keeps a zero-stock child selectable without falling back to buying the parent", async () => {
    mocks.inventory.mockResolvedValue({ projectId: 1, totalAvailable: 5, products: [{
      productType: "proto", totalAvailable: 5, publicAvailable: 3,
      suffixes: [{ suffix: "proton.me", totalAvailable: 5, publicAvailable: 3 }],
    }] });
    const { container } = render(<Dashboard />);
    let row: HTMLButtonElement | undefined;
    await waitFor(() => {
      row = productRows(container).find((item) => item.querySelector(".workbench-product-suffix")?.textContent === "@protonmail.com");
      expect(row).toHaveTextContent("Stock 0");
    });
    fireEvent.click(row!);
    expect(screen.getByTestId("selected-selector")).toHaveTextContent("protonmail.com");
    const create = screen.getByRole("button", { name: "Create fixture order" });
    expect(create).toBeDisabled();
    fireEvent.click(create);
    expect(mocks.createOrder).not.toHaveBeenCalled();
    expect(mocks.createOrderBatch).not.toHaveBeenCalled();
  });
});
