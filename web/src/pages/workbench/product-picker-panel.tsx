import { Card, Empty, Input, Tag } from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { ShoppingCart, Zap } from "lucide-react";
import { Fragment } from "react";
import { useTranslation } from "react-i18next";

import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { effectivePriceMultiplier } from "@/lib/membership";
import { cn } from "@/lib/utils";

import { calculateDiscountedLedgerTotal } from "./money";
import type {
  InventoryScope,
  ServiceMode,
  WorkbenchProduct,
  WorkbenchProject,
} from "./types";
import {
  formatCompactNumber,
  formatMoney,
  formatMoneyExact,
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
  if (inventoryScope === "public_only") {
    return serviceMode === "code"
      ? product.codePublicInventory
      : product.purchasePublicInventory;
  }
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
  priceMultiplier,
  productSearch,
  products,
  selectedProductId,
  serviceMode,
  selectedProject,
}: {
  inventoryScope: InventoryScope;
  onInventoryScopeChange: (value: InventoryScope) => void;
  onProductSearchChange: (value: string) => void;
  onSelectProduct: (productKey: string) => void;
  onServiceModeChange: (value: ServiceMode) => void;
  priceMultiplier: number;
  productSearch: string;
  products: WorkbenchProduct[];
  selectedProductId: string;
  serviceMode: ServiceMode;
  selectedProject?: WorkbenchProject;
}) {
  const { t } = useTranslation();
  const isGmailProduct = (product: WorkbenchProduct) =>
    product.productType === "gmail" || product.productType === "gmail_variant";
  const gmailProducts = products.filter(isGmailProduct).sort((left, right) => {
    if (left.productType === right.productType) return 0;
    return left.productType === "gmail" ? -1 : 1;
  });
  const firstGmailIndex = products.findIndex(isGmailProduct);
  const displayedProducts =
    firstGmailIndex < 0
      ? products
      : [
          ...products.slice(0, firstGmailIndex),
          ...gmailProducts,
          ...products
            .slice(firstGmailIndex)
            .filter((product) => !isGmailProduct(product)),
        ];

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
          displayedProducts.map((product, index) => {
            const selected = selectedProductId === product.id;
            const gmailChild = isGmailProduct(product);
            const inventory = getScopedInventory(product, serviceMode, inventoryScope);
            const originalPrice = getPrice(product, serviceMode);
            const discountedPrice = calculateDiscountedLedgerTotal(
              originalPrice,
              effectivePriceMultiplier(priceMultiplier, product.priceMultiplier),
            );
            const hasDiscount = discountedPrice < originalPrice;
            const priceLabel = hasDiscount
              ? `${t("Original price")} ${formatMoneyExact(originalPrice)}, ${t("Discounted price")} ${formatMoneyExact(discountedPrice)}`
              : formatMoneyExact(originalPrice);
            const suffix =
              product.productType === "gmail_variant"
                ? t(product.suffix)
                : product.suffix;
            return (
              <Fragment key={product.id}>
                {index === firstGmailIndex ? (
                  <div aria-level={3} className="workbench-product-group" role="heading">
                    @gmail
                  </div>
                ) : null}
                <button
                  className={cn(
                    "workbench-product-row",
                    (gmailChild || product.id !== product.productType) && "is-suffix",
                    selected && "is-selected"
                  )}
                  onClick={() => onSelectProduct(product.id)}
                  type="button"
                >
                  <span className="workbench-product-main">
                    <span className="workbench-product-title">
                      <OverflowTooltip content={t(product.label)}>
                        {t(product.label)}
                      </OverflowTooltip>
                      {gmailChild ? null : (
                        <Tag color="grey" shape="circle" size="small">
                          {productTypeLabel(product.productType, t)}
                        </Tag>
                      )}
                    </span>
                    <OverflowTooltip
                      className="workbench-product-suffix"
                      content={suffix}
                    >
                      {suffix}
                    </OverflowTooltip>
                  </span>
                  <span className="workbench-product-side">
                    <span
                      aria-label={priceLabel}
                      className="workbench-product-prices"
                    >
                      {hasDiscount ? (
                        <span className="workbench-original-price">
                          {formatMoney(originalPrice)}
                        </span>
                      ) : null}
                      <span className="workbench-product-price">
                        {formatMoney(discountedPrice)}
                      </span>
                    </span>
                    <span className="workbench-product-stock">
                      {t("Stock")} {formatCompactNumber(inventory)}
                    </span>
                  </span>
                </button>
              </Fragment>
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
