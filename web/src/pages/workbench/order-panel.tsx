import {
  Button,
  Card,
  Empty,
  Input,
  InputNumber,
  Tag,
  Typography,
} from "@douyinfe/semi-ui";
import { ChevronDown, ChevronUp, Mail, ShoppingCart, Zap } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { cn } from "@/lib/utils";

import { FetchControl } from "./fetch-control";
import { ProjectIcon } from "./project-icon";
import type {
  FetchResult,
  FetchSource,
  InventoryScope,
  ServiceMode,
  WorkbenchOrder,
  WorkbenchProduct,
  WorkbenchProject,
} from "./types";
import {
  buildPickupUrl,
  formatDateTime,
  formatMoney,
  formatRemainingDuration,
  inventoryScopeLabel,
  maskMiddle,
  maskSecret,
  productTypeLabel,
  serviceModeLabel,
  serviceStateMeta,
} from "./utils";

const { Text } = Typography;

function getPrice(product: WorkbenchProduct, serviceMode: ServiceMode, quantity: number) {
  return serviceMode === "code"
    ? product.codePrice * Math.max(1, quantity)
    : product.purchasePrice * Math.max(1, quantity);
}

function OrderAccordionItem({
  expanded,
  onFetchMail,
  onOpenMailbox,
  onToggle,
  order,
  product,
  project,
}: {
  expanded: boolean;
  onFetchMail: (order: WorkbenchOrder, source: FetchSource) => FetchResult | Promise<FetchResult>;
  onOpenMailbox: (params: { email: string; orderNo: string; token: string }) => void;
  onToggle: () => void;
  order: WorkbenchOrder;
  product?: WorkbenchProduct;
  project?: WorkbenchProject;
}) {
  const { t } = useTranslation();
  const state = serviceStateMeta(order.serviceState, t);
  const pickupUrl = buildPickupUrl(order.deliveryEmail, order.token);
  const maskedToken = maskSecret(order.token);
  const maskedPickupUrl = maskMiddle(
    buildPickupUrl(order.deliveryEmail, maskedToken),
    24,
    12
  );
  const openMailbox = () =>
    onOpenMailbox({
      email: order.deliveryEmail,
      orderNo: order.orderNo,
      token: order.token,
    });
  const receiveLabel =
    order.serviceMode === "code" ? t("Receive until") : t("Activation until");
  const receiveValue =
    order.serviceMode === "code" ? order.receiveUntil : order.activationUntil;

  return (
    <div className={cn("workbench-order-item", expanded && "is-expanded")}>
      <div className="workbench-order-summary">
        <button className="workbench-order-summary-info" onClick={onToggle} type="button">
          <ProjectIcon name={project?.name ?? "-"} logoUrl={project?.logoUrl} size={24} />
          <span className="workbench-order-summary-main">
            <span className="workbench-order-summary-title">
              <OverflowTooltip className="truncate" content={project?.name ?? "-"}>
                {project?.name ?? "-"}
              </OverflowTooltip>
              <Tag color="orange" shape="circle" size="small">
                {serviceModeLabel(order.serviceMode, t)}
              </Tag>
              <Tag color={state.color} shape="circle" size="small">
                {state.label}
              </Tag>
              {product ? (
                <Tag color="grey" shape="circle" size="small">
                  {productTypeLabel(product.productType, t)}
                </Tag>
              ) : null}
            </span>
            <span className="workbench-order-summary-subtitle">
              <OverflowTooltip
                className="font-mono-data"
                content={order.deliveryEmail}
              >
                {order.deliveryEmail}
              </OverflowTooltip>
              <OverflowTooltip content={order.orderNo}>{order.orderNo}</OverflowTooltip>
            </span>
          </span>
        </button>
        <Button
          className="workbench-order-summary-mail"
          icon={<Mail size={14} />}
          onClick={openMailbox}
          size="small"
          theme="outline"
          type="tertiary"
        >
          {t("Open mailbox")}
        </Button>
        <button className="workbench-order-summary-state" onClick={onToggle} type="button">
          <span className="workbench-order-summary-side">
            <strong>
              {order.verificationCode ??
                (order.serviceMode === "purchase" ? formatMoney(order.payAmount) : t("Waiting"))}
            </strong>
            <small>{formatRemainingDuration(order.afterSaleUntil)}</small>
          </span>
          {expanded ? <ChevronUp size={16} /> : <ChevronDown size={16} />}
        </button>
      </div>

      {expanded ? (
        <div className="workbench-order-detail">
          <div className="workbench-order-detail-grid">
            <div className="workbench-delivery-line">
              <div className="min-w-0">
                <div className="workbench-mini-label">{t("Delivery email")}</div>
                <Text
                  className="workbench-copyable-value font-mono-data text-[14px] font-bold"
                  copyable={createCopyableConfig(order.deliveryEmail, t("Copied"))}
                >
                  <OverflowTooltip content={order.deliveryEmail}>
                    {order.deliveryEmail}
                  </OverflowTooltip>
                </Text>
              </div>
            </div>

            <div className="workbench-code-line">
              <div className="workbench-code-content">
                <div className="workbench-mini-label">
                  {order.serviceMode === "code"
                    ? t("Latest verification code")
                    : t("Quick verification code")}
                </div>
                <div className="workbench-code-value-row">
                  <Text
                    className={cn(
                      "workbench-inline-code",
                      !order.verificationCode && "is-empty"
                    )}
                    copyable={
                      order.verificationCode
                        ? createCopyableConfig(order.verificationCode, t("Copied"))
                        : false
                    }
                  >
                    {order.verificationCode ?? t("Waiting")}
                  </Text>
                </div>
                {!order.verificationCode ? (
                  <FetchControl
                    actionLabelKey="Refresh"
                    compact
                    onFetch={(source) => onFetchMail(order, source)}
                    variant="code"
                  />
                ) : null}
              </div>
            </div>
          </div>

          <div className="workbench-order-meta-grid">
            <div className="workbench-order-meta-item">
              <span>{receiveLabel}</span>
              <strong>{formatDateTime(receiveValue)}</strong>
            </div>
            <div className="workbench-order-meta-item">
              <span>{t("After-sales until")}</span>
              <strong>{formatDateTime(order.afterSaleUntil)}</strong>
            </div>
            <div className="workbench-order-meta-item is-wide">
              <span>{t("Service Token")}</span>
              <Text
                className="workbench-copyable-value font-mono-data font-bold"
                copyable={createCopyableConfig(order.token, t("Copied"))}
              >
                <OverflowTooltip content={maskedToken}>
                  {maskedToken}
                </OverflowTooltip>
              </Text>
            </div>
            <div className="workbench-order-meta-item is-wide">
              <span>{t("Pickup URL")}</span>
              <Text
                className="workbench-copyable-value font-mono-data font-bold"
                copyable={createCopyableConfig(pickupUrl, t("Copied"))}
              >
                <OverflowTooltip content={maskedPickupUrl}>
                  {maskedPickupUrl}
                </OverflowTooltip>
              </Text>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

export function OrderPanel({
  creating = false,
  inventoryScope,
  onCreateOrder,
  onFetchOrderMail,
  onOpenMailbox,
  onQuantityChange,
  onSearchChange,
  onSelectOrder,
  orders,
  orderSearch,
  productsById,
  projectsById,
  quantity,
  selectedOrder,
  selectedProduct,
  selectedProductInventory,
  selectedProject,
  serviceMode,
}: {
  creating?: boolean;
  inventoryScope: InventoryScope;
  onCreateOrder: () => void;
  onFetchOrderMail: (
    order: WorkbenchOrder,
    source: FetchSource
  ) => FetchResult | Promise<FetchResult>;
  onOpenMailbox: (params: { email: string; orderNo: string; token: string }) => void;
  onQuantityChange: (value: number) => void;
  onSearchChange: (value: string) => void;
  onSelectOrder: (orderNo: string) => void;
  orders: WorkbenchOrder[];
  orderSearch: string;
  productsById: Map<string, WorkbenchProduct>;
  projectsById: Map<string, WorkbenchProject>;
  quantity: number;
  selectedOrder?: WorkbenchOrder;
  selectedProduct?: WorkbenchProduct;
  selectedProductInventory: number;
  selectedProject?: WorkbenchProject;
  serviceMode: ServiceMode;
}) {
  const { t } = useTranslation();
  const safeQuantity =
    selectedProductInventory > 0
      ? Math.min(Math.max(1, quantity), selectedProductInventory)
      : 0;
  const totalPrice = selectedProduct
    ? getPrice(selectedProduct, serviceMode, safeQuantity)
    : 0;
  const inventory = selectedProductInventory;
  const stockText = `${t("Stock")} ${inventory}`;
  const visibleOrders = useMemo(
    () => orders.filter((order) => order.status !== "refunded"),
    [orders]
  );
  const minQuantity = inventory > 0 ? 1 : 0;

  return (
    <Card className="workbench-column workbench-order-panel" shadows="hover">
      <div className="workbench-order-panel-scroll">
        <section className="workbench-order-entry">
          <div className="workbench-quick-order-row">
            <div className="workbench-quick-product">
              <ProjectIcon
                logoUrl={selectedProject?.logoUrl}
                name={selectedProject?.name ?? "-"}
                size={30}
              />
            </div>
            <span className="workbench-quick-meta-pill">
              {inventoryScopeLabel(inventoryScope, t)}
            </span>
            <span className="workbench-quick-meta-pill workbench-quick-stock">
              {stockText}
            </span>
            <span className="workbench-quick-meta-pill">
              {serviceMode === "code"
                ? `${selectedProduct?.codeWindowMinutes ?? 0}m`
                : `${selectedProduct?.warrantyHours ?? 0}h`}
            </span>
            <strong className="workbench-quick-price">{formatMoney(totalPrice)}</strong>
            <InputNumber
              className="workbench-quantity-input"
              disabled={!selectedProduct || inventory <= 0 || creating}
              max={inventory}
              min={minQuantity}
              onChange={(value) => {
                const parsed = Number(value);
                onQuantityChange(Number.isFinite(parsed) ? parsed : minQuantity);
              }}
              precision={0}
              showClear={false}
              step={1}
              style={{ width: "100%" }}
              value={safeQuantity}
            />
            <Button
              className="workbench-create-order-button"
              disabled={!selectedProject || !selectedProduct || inventory <= 0 || creating}
              icon={serviceMode === "code" ? <Zap size={16} /> : <ShoppingCart size={16} />}
              loading={creating}
              onClick={onCreateOrder}
              theme="solid"
              type="primary"
            >
              {serviceMode === "code" ? t("Order code now") : t("Buy now")}
            </Button>
          </div>
        </section>

        <section className="workbench-section">
          <div className="workbench-section-title">
            <div>
              <strong>{t("Orders")}</strong>
              <span>{t("Order-scoped mail and code result")}</span>
            </div>
            <Input
              className="resources-search-input workbench-order-search"
              onChange={(value) => onSearchChange(String(value))}
              placeholder={t("Search project or email")}
              showClear
              value={orderSearch}
            />
          </div>

          <div className="workbench-order-accordion">
            {visibleOrders.length === 0 ? (
              <Empty description={t("No order resources")} />
            ) : (
              visibleOrders.map((order) => (
                <OrderAccordionItem
                  expanded={selectedOrder?.orderNo === order.orderNo}
                  key={order.orderNo}
                  onFetchMail={onFetchOrderMail}
                  onOpenMailbox={onOpenMailbox}
                  onToggle={() => onSelectOrder(order.orderNo)}
                  order={order}
                  product={productsById.get(order.productId)}
                  project={projectsById.get(order.projectId)}
                />
              ))
            )}
          </div>
        </section>
      </div>
    </Card>
  );
}
