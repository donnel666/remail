import { useCallback, useEffect, useRef, useState } from "react";
import {
  Button,
  Input,
  Modal,
  RadioGroup,
  Radio,
  Spin,
  Tag,
  TextArea,
  Toast,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { FileImage, Paperclip, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { listOrders, type OrderResponse } from "@/lib/orders-api";
import { formatLedgerAmount } from "@/pages/orders/order-meta";
import { ProjectIcon } from "@/pages/workbench/project-icon";

import {
  buildOrderRefFromOrder,
  type TicketOrderRef,
} from "../orders/ticket-order-handoff";
import { formatTicketAmount } from "./ticket-meta";
import {
  createTicket,
  getOrderAfterSaleState,
  type MockTicket,
  type MockTicketType,
} from "./tickets-mock";

interface PendingAttachment {
  id: string;
  name: string;
  dataUrl: string;
}

function readImageAsDataUrl(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () =>
      typeof reader.result === "string"
        ? resolve(reader.result)
        : reject(new Error("Unable to read attachment."));
    reader.onerror = () =>
      reject(reader.error ?? new Error("Unable to read attachment."));
    reader.readAsDataURL(file);
  });
}

export function CreateTicketModal({
  open,
  onOpenChange,
  initialOrder,
  onCreated,
  onViewTicket,
}: {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  initialOrder?: TicketOrderRef | null;
  onCreated: () => void;
  onViewTicket: (ticket: MockTicket) => void;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const attachmentReadIdRef = useRef(0);

  const [ticketType, setTicketType] = useState<MockTicketType>("order");
  const [selectedOrder, setSelectedOrder] =
    useState<TicketOrderRef | null>(null);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [attachments, setAttachments] = useState<PendingAttachment[]>([]);
  const [orderSearch, setOrderSearch] = useState("");
  const [debouncedOrderSearch, flushOrderSearch] =
    useDebouncedValue(orderSearch);
  const [orders, setOrders] = useState<OrderResponse[]>([]);
  const [ordersLoading, setOrdersLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const resetState = useCallback(() => {
    attachmentReadIdRef.current += 1;
    setTicketType("order");
    setSelectedOrder(null);
    setTitle("");
    setDescription("");
    setAttachments([]);
    setOrderSearch("");
    flushOrderSearch("");
    setSubmitting(false);
  }, [flushOrderSearch]);

  useEffect(() => {
    if (!open) {
      attachmentReadIdRef.current += 1;
      return;
    }
    resetState();
    if (initialOrder) {
      setTicketType("order");
      setSelectedOrder(initialOrder);
    }
  }, [initialOrder, open, resetState]);

  useEffect(() => {
    if (!open || ticketType !== "order") return;
    let cancelled = false;
    setOrdersLoading(true);
    listOrders({ search: debouncedOrderSearch.trim() || undefined, limit: 40 })
      .then((response) => {
        if (!cancelled) setOrders(response.items);
      })
      .catch(() => {
        if (!cancelled) setOrders([]);
      })
      .finally(() => {
        if (!cancelled) setOrdersLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [debouncedOrderSearch, open, ticketType]);

  const canSubmit =
    title.trim().length > 0 &&
    description.trim().length > 0 &&
    (ticketType === "general" || selectedOrder !== null);

  const switchType = (type: MockTicketType) => {
    if (type === ticketType) return;
    setTicketType(type);
    if (type === "general") setSelectedOrder(null);
  };

  const handlePickFiles = async (files: FileList | null) => {
    if (!files) return;
    const readId = attachmentReadIdRef.current;
    const selected = Array.from(files)
      .filter((file) => file.type.startsWith("image/"))
      .slice(0, Math.max(0, 3 - attachments.length));
    try {
      const next = await Promise.all(
        selected.map(async (file, index) => ({
          id: `${file.name}-${file.size}-${file.lastModified}-${index}`,
          name: file.name,
          dataUrl: await readImageAsDataUrl(file),
        }))
      );
      if (attachmentReadIdRef.current !== readId) return;
      setAttachments((current) => [...current, ...next].slice(0, 3));
    } catch {
      if (attachmentReadIdRef.current === readId) {
        Toast.error(t("Ticket create failed."));
      }
    } finally {
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  };

  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      const ticket = await createTicket({
        ticketType,
        title: title.trim(),
        firstMessage: description.trim(),
        order: ticketType === "order" ? selectedOrder ?? undefined : undefined,
        attachments:
          attachments.length > 0
            ? attachments.map((attachment) => attachment.dataUrl)
            : undefined,
      });
      onCreated();
      onOpenChange(false);
      onViewTicket(ticket);
    } catch {
      Toast.error(t("Ticket create failed."));
    } finally {
      setSubmitting(false);
    }
  };

  const orderPicker =
    ticketType !== "order" ? null : selectedOrder ? (
      <div className="flex items-center gap-2.5 rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2.5">
        <ProjectIcon
          logoUrl={selectedOrder.projectLogoUrl}
          name={selectedOrder.projectName}
          size={28}
        />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="text-[13px] font-semibold text-[var(--semi-color-text-0)]">
              {selectedOrder.projectName}
            </span>
            <Tag color="white" shape="circle" size="small">
              {selectedOrder.serviceMode === "code"
                ? t("Code receiving")
                : t("Purchase")}
            </Tag>
            <span className="font-mono-data text-xs text-[var(--semi-color-text-2)]">
              {formatTicketAmount(selectedOrder.payAmount)}
            </span>
          </div>
          <OverflowTooltip
            className="font-mono-data mt-0.5 block max-w-full text-xs text-[var(--semi-color-text-2)]"
            content={selectedOrder.deliveryEmail}
          >
            {selectedOrder.deliveryEmail}
          </OverflowTooltip>
        </div>
        <Button
          size="small"
          type="tertiary"
          onClick={() => setSelectedOrder(null)}
        >
          {t("Change order")}
        </Button>
      </div>
    ) : (
      <div>
        <Input
          prefix={<IconSearch />}
          placeholder={t("Search order or email")}
          showClear
          size="small"
          value={orderSearch}
          onChange={(value) => setOrderSearch(String(value))}
          className="resources-search-input mb-2"
        />
        <div className="flex max-h-60 flex-col gap-1.5 overflow-auto rounded-xl border border-[var(--semi-color-border)] p-2">
          {ordersLoading ? (
            <div className="flex justify-center py-6">
              <Spin />
            </div>
          ) : orders.length === 0 ? (
            <div className="py-6 text-center text-xs text-[var(--semi-color-text-2)]">
              {t("No matching orders")}
            </div>
          ) : (
            orders.slice(0, 12).map((order) => {
              const state = getOrderAfterSaleState(order);
              const disabled = !state.eligible;
              return (
                <button
                  key={order.orderNo}
                  type="button"
                  disabled={disabled}
                  onClick={() => setSelectedOrder(buildOrderRefFromOrder(order))}
                  className={`flex min-w-0 items-center gap-2.5 rounded-lg border border-transparent px-2.5 py-2 text-left transition-colors ${
                    disabled
                      ? "cursor-not-allowed opacity-55"
                      : "hover:border-[var(--semi-color-primary-light-active)] hover:bg-[var(--semi-color-fill-0)]"
                  }`}
                >
                  <ProjectIcon
                    logoUrl={order.projectLogoUrl ?? undefined}
                    name={order.projectName || "-"}
                    size={24}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-[13px] font-medium text-[var(--semi-color-text-0)]">
                        {order.projectName || "-"}
                      </span>
                      <span className="font-mono-data text-xs text-[var(--semi-color-text-2)]">
                        {formatLedgerAmount(order.payAmount)}
                      </span>
                    </div>
                    <OverflowTooltip
                      className="font-mono-data block max-w-full text-xs text-[var(--semi-color-text-2)]"
                      content={order.deliveryEmail}
                    >
                      {order.deliveryEmail}
                    </OverflowTooltip>
                  </div>
                  <Tag
                    color={state.eligible ? "green" : "grey"}
                    shape="circle"
                    size="small"
                  >
                    {state.eligible
                      ? t("In after-sale window")
                      : state.reason === "expired"
                        ? t("After-sale expired")
                        : state.reason === "refunded"
                          ? t("Refunded")
                          : t("Not delivered yet")}
                  </Tag>
                </button>
              );
            })
          )}
        </div>
      </div>
    );

  const footer = (
    <div className="flex justify-end gap-2">
      <Button disabled={submitting} type="tertiary" onClick={() => onOpenChange(false)}>
        {t("Cancel")}
      </Button>
      <Button
        disabled={!canSubmit}
        loading={submitting}
        type="primary"
        onClick={() => void submit()}
      >
        {t("Submit")}
      </Button>
    </div>
  );

  return (
    <Modal
      centered
      footer={footer}
      maskClosable={false}
      onCancel={() => onOpenChange(false)}
      title={t("Create ticket")}
      visible={open}
      width={isMobile ? "94%" : 560}
    >
      <div className="flex flex-col gap-4 pb-1">
        <div>
          <div className="mb-2 text-[13px] font-semibold text-[var(--semi-color-text-0)]">
            {t("Ticket type")}
          </div>
          <RadioGroup
            type="button"
            buttonSize="middle"
            value={ticketType}
            onChange={(event) => switchType(event.target.value as MockTicketType)}
          >
            <Radio value="order">{t("Order ticket")}</Radio>
            <Radio value="general">{t("General ticket")}</Radio>
          </RadioGroup>
        </div>

        {ticketType === "order" ? (
          <div>
            <div className="mb-2 flex items-center gap-2 text-[13px] font-semibold text-[var(--semi-color-text-0)]">
              {t("Related order")}
              <span className="text-xs font-normal text-[var(--semi-color-text-3)]">
                {t("Pick the order that needs help")}
              </span>
            </div>
            {orderPicker}
          </div>
        ) : null}

        <div>
          <div className="mb-2 text-[13px] font-semibold text-[var(--semi-color-text-0)]">
            {t("Ticket title")}
          </div>
          <Input
            maxLength={60}
            placeholder={t("Ticket title placeholder")}
            showClear
            value={title}
            onChange={(value) => setTitle(String(value))}
          />
        </div>

        <div>
          <div className="mb-2 text-[13px] font-semibold text-[var(--semi-color-text-0)]">
            {t("Description")}
          </div>
          <TextArea
            autosize={{ minRows: 3, maxRows: 6 }}
            maxCount={500}
            placeholder={t("Description placeholder")}
            showClear
            value={description}
            onChange={(value) => setDescription(String(value))}
          />
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Button
              icon={<Paperclip size={14} />}
              size="small"
              type="tertiary"
              disabled={attachments.length >= 3}
              onClick={() => fileInputRef.current?.click()}
            >
              {t("Add screenshot")}
            </Button>
            <span className="text-xs text-[var(--semi-color-text-3)]">
              {t("Attachment hint")}
            </span>
            <input
              ref={fileInputRef}
              type="file"
              accept="image/*"
              multiple
              hidden
              onChange={(event) => void handlePickFiles(event.target.files)}
            />
          </div>
          {attachments.length > 0 ? (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {attachments.map((attachment, index) => (
                <Tag
                  key={attachment.id}
                  color="white"
                  shape="circle"
                  className="max-w-[220px]"
                >
                  <span className="flex min-w-0 items-center gap-1">
                    <FileImage size={12} className="shrink-0" />
                    <span className="truncate">{attachment.name}</span>
                    <X
                      size={12}
                      className="shrink-0 cursor-pointer"
                      onClick={() =>
                        setAttachments((current) =>
                          current.filter((_, itemIndex) => itemIndex !== index)
                        )
                      }
                    />
                  </span>
                </Tag>
              ))}
            </div>
          ) : null}
        </div>
      </div>
    </Modal>
  );
}
