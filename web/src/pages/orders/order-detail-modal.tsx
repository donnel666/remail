import type { ReactNode } from "react";
import { Button, Modal, Tag, Toast, Typography } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { copyText } from "@/lib/clipboard";
import {
  buildPickupUrl,
  maskMiddle,
  maskSecret,
  productTypeLabel,
} from "@/pages/workbench/utils";

import {
  formatLedgerAmount,
  formatOrderDateTime,
  renderOrderStatusTag,
  renderServiceModeTag,
} from "./order-meta";
import type { MockOrder } from "./orders-mock";

const { Text } = Typography;

function DetailRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div
      className="flex items-center justify-between gap-4 border-b border-dashed py-2 last:border-b-0"
      style={{ borderColor: "var(--semi-color-border)" }}
    >
      <span className="shrink-0 text-[13px] text-[var(--semi-color-text-2)] select-none">
        {label}
      </span>
      <div className="flex min-w-0 items-center justify-end text-[13px] text-[var(--semi-color-text-0)]">
        {value}
      </div>
    </div>
  );
}

function CopyableValue({
  copiedText,
  copyContent,
  text,
}: {
  copiedText: string;
  copyContent?: string;
  text: string;
}) {
  return (
    <span className="remail-copyable-table-text">
      <Text copyable={createCopyableConfig(copyContent ?? text, copiedText)}>
        <OverflowTooltip
          className="remail-copyable-table-text-content font-mono-data"
          content={text}
        >
          {text}
        </OverflowTooltip>
      </Text>
    </span>
  );
}

function SecretValue({
  copyContent,
  text,
}: {
  copyContent: string;
  text: string;
}) {
  const { t } = useTranslation();

  const handleCopy = async () => {
    try {
      await copyText(copyContent);
      Toast.success(t("Copied"));
    } catch {
      Toast.error(t("Copy failed."));
    }
  };

  return (
    <div className="flex min-w-0 items-center gap-2">
      <OverflowTooltip
        className="remail-copyable-table-text-content font-mono-data"
        content={text}
      >
        {text}
      </OverflowTooltip>
      <Button
        size="small"
        type="tertiary"
        onClick={() => void handleCopy()}
      >
        {t("Copy")}
      </Button>
    </div>
  );
}

export function OrderDetailModal({
  onClose,
  order,
}: {
  onClose: () => void;
  order: MockOrder | null;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();

  const receiveUntilLabel =
    order?.serviceMode === "purchase" && !order.activatedAt
      ? t("Activation until")
      : t("Receive until");
  const pickupUrl =
    order?.serviceToken !== undefined
      ? buildPickupUrl(order.deliveryEmail, order.serviceToken)
      : undefined;

  return (
    <Modal
      centered
      footer={null}
      onCancel={onClose}
      title={t("Order detail")}
      visible={Boolean(order)}
      width={isMobile ? "94%" : 580}
    >
      {order ? (
        <div className="pb-2">
          <div className="mb-3 flex flex-wrap items-center gap-2">
            {renderServiceModeTag(order.serviceMode, t)}
            {renderOrderStatusTag(order.status, t)}
            <Tag color="white" shape="circle">
              {productTypeLabel(order.productType, t)}
            </Tag>
            <Tag color="white" shape="circle">
              {order.clientChannel === "console" ? t("Console") : "API Key"}
            </Tag>
          </div>

          <DetailRow
            label={t("Delivery email")}
            value={
              <CopyableValue copiedText={t("Copied")} text={order.deliveryEmail} />
            }
          />
          <DetailRow
            label={t("Order No")}
            value={<CopyableValue copiedText={t("Copied")} text={order.orderNo} />}
          />
          <DetailRow label={t("Project")} value={order.projectName} />
          <DetailRow
            label={t("Pay amount")}
            value={
              <span className="font-mono-data font-semibold">
                {formatLedgerAmount(order.payAmount)}
              </span>
            }
          />
          {order.refundAmount > 0 ? (
            <DetailRow
              label={t("Refund amount")}
              value={
                <span className="font-mono-data font-semibold text-[var(--semi-color-danger)]">
                  {formatLedgerAmount(order.refundAmount)}
                </span>
              }
            />
          ) : null}
          <DetailRow
            label={t("Code")}
            value={
              order.verificationCode ? (
                <CopyableValue
                  copiedText={t("Copied")}
                  text={order.verificationCode}
                />
              ) : order.status === "active" ? (
                <Tag color="grey" shape="circle">
                  {t("Waiting")}
                </Tag>
              ) : (
                <span className="text-[var(--semi-color-text-3)]">-</span>
              )
            }
          />
          <DetailRow
            label={receiveUntilLabel}
            value={formatOrderDateTime(order.receiveUntil)}
          />
          {order.activatedAt ? (
            <DetailRow
              label={t("Activated at")}
              value={formatOrderDateTime(order.activatedAt)}
            />
          ) : null}
          {order.afterSaleUntil ? (
            <DetailRow
              label={t("After-sales until")}
              value={formatOrderDateTime(order.afterSaleUntil)}
            />
          ) : null}
          {order.serviceToken ? (
            <DetailRow
              label={t("Service Token")}
              value={
                <SecretValue
                  copyContent={order.serviceToken}
                  text={maskSecret(order.serviceToken)}
                />
              }
            />
          ) : null}
          {pickupUrl ? (
            <DetailRow
              label={t("Pickup URL")}
              value={
                <SecretValue
                  copyContent={pickupUrl}
                  text={maskMiddle(pickupUrl, 26, 10)}
                />
              }
            />
          ) : null}
          <DetailRow
            label={t("Created at")}
            value={formatOrderDateTime(order.createdAt)}
          />
        </div>
      ) : null}
    </Modal>
  );
}
