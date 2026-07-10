import { Card, Empty, Input, Tag } from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { ShoppingCart, Zap } from "lucide-react";
import { useTranslation } from "react-i18next";

import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { cn } from "@/lib/utils";

import type {
  InventoryScope,
  ServiceMode,
  WorkbenchProduct,
  WorkbenchProject,
} from "./types";
import {
  formatCompactNumber,
  formatMoney,
  productTypeLabel,
} from "./utils";

function getInventory(product: WorkbenchProduct, serviceMode: ServiceMode) {
  return serviceMode === "code"
    ? product.codeInventory
    : product.purchaseInventory;
}

function getScopedInventory(
  product: WorkbenchProduct,
  serviceMode: ServiceMode,
  inventoryScope: InventoryScope
) {
  if (inventoryScope === "public_only") return product.publicInventory;
  return getInventory(product, serviceMode);
}

function getPrice(product: WorkbenchProduct, serviceMode: ServiceMode) {
  return serviceMode === "code" ? product.codePrice : product.purchasePrice;
}

export function ProductPickerPanel({
  inventoryScope,
  onInventoryScopeChange,
  onProductSearchChange,
  onSelectProduct,
  onServiceModeChange,
  productSearch,
  products,
  selectedProductId,
  serviceMode,
  selectedProject,
}: {
  inventoryScope: InventoryScope;
  onInventoryScopeChange: (value: InventoryScope) => void;
  onProductSearchChange: (value: string) => void;
  onSelectProduct: (productId: string) => void;
  onServiceModeChange: (value: ServiceMode) => void;
  productSearch: string;
  products: WorkbenchProduct[];
  selectedProductId: string;
  serviceMode: ServiceMode;
  selectedProject?: WorkbenchProject;
}) {
  const { t } = useTranslation();

  return (
    <Card className="workbench-column workbench-product-panel" shadows="hover">
      <div className="workbench-service-tabs" role="tablist">
        <button
          className={cn("workbench-service-tab", serviceMode === "purchase" && "is-active")}
          onClick={() => onServiceModeChange("purchase")}
          type="button"
        >
          <ShoppingCart size={15} />
          {t("Purchase")}
        </button>
        <button
          className={cn("workbench-service-tab", serviceMode === "code" && "is-active")}
          onClick={() => onServiceModeChange("code")}
          type="button"
        >
          <Zap size={15} />
          {t("Code receiving")}
        </button>
      </div>

      <div className="workbench-scope-row">
        <button
          className={cn(
            "workbench-scope-button",
            inventoryScope === "private_first" && "is-active"
          )}
          onClick={() => onInventoryScopeChange("private_first")}
          type="button"
        >
          {t("Private first")}
        </button>
        <button
          className={cn(
            "workbench-scope-button",
            inventoryScope === "public_only" && "is-active"
          )}
          onClick={() => onInventoryScopeChange("public_only")}
          type="button"
        >
          {t("Public only")}
        </button>
      </div>

      <Input
        className="resources-search-input workbench-panel-search"
        onChange={(value) => onProductSearchChange(String(value))}
        placeholder={t("Search suffix")}
        prefix={<IconSearch />}
        showClear
        value={productSearch}
      />

      <div className="workbench-product-list">
        {products.length === 0 ? (
          <Empty description={t("No products")} />
        ) : (
          products.map((product) => {
            const selected = selectedProductId === product.id;
            const inventory = getScopedInventory(product, serviceMode, inventoryScope);
            return (
              <button
                className={cn(
                  "workbench-product-row",
                  product.emailSuffix && "is-suffix",
                  selected && "is-selected"
                )}
                key={product.id}
                onClick={() => onSelectProduct(product.id)}
                type="button"
              >
                <span className="workbench-product-main">
                  <span className="workbench-product-title">
                    <OverflowTooltip content={product.label}>
                      {product.label}
                    </OverflowTooltip>
                    <Tag color="grey" shape="circle" size="small">
                      {productTypeLabel(product.productType, t)}
                    </Tag>
                  </span>
                  <OverflowTooltip
                    className="workbench-product-suffix"
                    content={product.suffix}
                  >
                    {product.suffix}
                  </OverflowTooltip>
                </span>
                <span className="workbench-product-side">
                  <span className="workbench-product-price">
                    {formatMoney(getPrice(product, serviceMode))}
                  </span>
                  <span className="workbench-product-stock">
                    {t("Stock")} {formatCompactNumber(inventory)}
                  </span>
                </span>
              </button>
            );
          })
        )}
      </div>

      <div className="workbench-picker-footnote">
        <OverflowTooltip content={selectedProject?.name ?? "-"}>
          {selectedProject?.name ?? "-"}
        </OverflowTooltip>
        <OverflowTooltip
          content={
            serviceMode === "code"
              ? t("Short-lived mailbox can receive one email only.")
              : t("Long-lived purchase can receive mail repeatedly.")
          }
        >
          {serviceMode === "code"
            ? t("Short-lived mailbox can receive one email only.")
            : t("Long-lived purchase can receive mail repeatedly.")}
        </OverflowTooltip>
      </div>
    </Card>
  );
}
