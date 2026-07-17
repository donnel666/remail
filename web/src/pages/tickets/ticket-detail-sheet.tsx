import { useCallback, useEffect, useState } from "react";
import {
  Button,
  Modal,
  SideSheet,
  Spin,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { BadgeCheck, CheckCircle2, Undo2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { ProjectIcon } from "@/pages/workbench/project-icon";

import { TicketConversation } from "./ticket-conversation";
import {
  formatTicketAmount,
  formatTicketDateTime,
  renderTicketStatusTag,
  renderTicketTypeTag,
} from "./ticket-meta";
import {
  closeTicket,
  getTicket,
  markTicketRead,
  refundAndCloseTicket,
  type Ticket,
} from "./tickets-api";

const { Text } = Typography;

export function TicketDetailSheet({
  ticketNo,
  viewerRole,
  platformCanOperate = false,
  platformCanRefund = false,
  onClose,
  onChanged,
}: {
  ticketNo: string | null;
  viewerRole: "user" | "platform";
  platformCanOperate?: boolean;
  platformCanRefund?: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [ticket, setTicket] = useState<Ticket | null>(null);
  const [loading, setLoading] = useState(false);
  const [actionBusy, setActionBusy] = useState(false);

  useEffect(() => {
    if (!ticketNo) {
      setTicket(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    void getTicket(ticketNo)
      .then(async (detail) => {
        await markTicketRead(ticketNo, viewerRole);
        if (cancelled) return;
        setTicket(
          viewerRole === "user"
            ? { ...detail, requesterUnreadCount: 0 }
            : { ...detail, platformUnreadCount: 0 }
        );
      })
      .catch(() => {
        if (!cancelled) Toast.error(t("Ticket load failed."));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [t, ticketNo, viewerRole]);

  // Both replies and quick actions run through here so the list stays in sync.
  const applyUpdate = useCallback(
    (next: Ticket) => {
      setTicket(next);
      onChanged();
    },
    [onChanged]
  );

  const runAction = useCallback(
    async (action: () => Promise<Ticket>, successKey: string) => {
      setActionBusy(true);
      try {
        const next = await action();
        applyUpdate(next);
        Toast.success(t(successKey));
      } catch {
        Toast.error(t("Ticket operation failed."));
      } finally {
        setActionBusy(false);
      }
    },
    [applyUpdate, t]
  );

  const handleClose = (by: "user" | "platform") => {
    if (!ticket) return;
    if (by === "platform" && !platformCanOperate) return;
    Modal.confirm({
      title: t("Close ticket"),
      content: t("Close ticket confirm"),
      okText: t("Close ticket"),
      cancelText: t("Cancel"),
      onOk: () =>
        runAction(
          () => closeTicket(ticket.ticketNo, by),
          "Ticket closed successfully."
        ),
    });
  };

  const handleRefundAndClose = () => {
    if (!ticket || !platformCanRefund) return;
    const amount = ticket.order?.payAmount ?? 0;
    Modal.confirm({
      title: t("Refund and close"),
      content: t("Refund and close confirm", {
        amount: formatTicketAmount(amount),
      }),
      okText: t("Refund and close"),
      cancelText: t("Cancel"),
      onOk: () =>
        runAction(
          () => refundAndCloseTicket(ticket.ticketNo),
          "Ticket refunded and closed."
        ),
    });
  };

  const headerArea = ticket ? (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-2">
        <OverflowTooltip
          className="min-w-0 max-w-full text-[15px] font-semibold text-[var(--semi-color-text-0)]"
          content={ticket.title}
        >
          {ticket.title}
        </OverflowTooltip>
      </div>
      <div className="mt-1.5 flex min-w-0 flex-wrap items-center gap-2">
        {renderTicketStatusTag(ticket.status, t)}
        {renderTicketTypeTag(ticket.ticketType, t)}
        <Text
          className="font-mono-data text-xs"
          copyable={createCopyableConfig(ticket.ticketNo, t("Copied"))}
          type="tertiary"
        >
          {ticket.ticketNo}
        </Text>
        <span className="text-xs text-[var(--semi-color-text-3)]">
          {t("Created at")} {formatTicketDateTime(ticket.createdAt)}
        </span>
      </div>
    </div>
  ) : null;

  const orderCard =
    ticket?.order !== undefined ? (
      <div className="ticket-sheet-order">
        <ProjectIcon
          logoUrl={ticket.order.projectLogoUrl}
          name={ticket.order.projectName}
          size={26}
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <span className="text-[13px] font-semibold text-[var(--semi-color-text-0)]">
              {ticket.order.projectName}
            </span>
            <Tag color="white" shape="circle" size="small">
              {ticket.order.serviceMode === "code"
                ? t("Code receiving")
                : t("Purchase")}
            </Tag>
            <span className="font-mono-data text-xs font-semibold text-[var(--semi-color-text-1)]">
              {formatTicketAmount(ticket.order.payAmount)}
            </span>
          </div>
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 text-xs text-[var(--semi-color-text-2)]">
            <OverflowTooltip
              className="font-mono-data max-w-[200px]"
              content={ticket.order.deliveryEmail}
            >
              {ticket.order.deliveryEmail}
            </OverflowTooltip>
            <OverflowTooltip
              className="font-mono-data max-w-[190px]"
              content={ticket.order.orderNo}
            >
              {ticket.order.orderNo}
            </OverflowTooltip>
          </div>
        </div>
      </div>
    ) : null;

  const resolutionCard = ticket?.resolution ? (
    <div
      className={`ticket-resolution ${
        ticket.resolution.kind === "refunded" ? "is-success" : "is-neutral"
      }`}
    >
      {ticket.resolution.kind === "refunded" ? (
        <BadgeCheck size={16} className="shrink-0" />
      ) : (
        <CheckCircle2 size={16} className="shrink-0" />
      )}
      <div className="min-w-0 text-[13px] font-semibold">
        {ticket.resolution.kind === "refunded"
          ? t("Ticket refunded amount", {
              amount: formatTicketAmount(ticket.resolution.refundAmount ?? 0),
            })
          : t("Ticket has been closed")}
      </div>
    </div>
  ) : null;

  const platformActions =
    ticket &&
    viewerRole === "platform" &&
    ticket.status !== "closed" &&
    (platformCanOperate || platformCanRefund) ? (
      <div className="ticket-sheet-footer">
        {ticket.ticketType === "order" && platformCanRefund ? (
          <Button
            icon={<Undo2 size={14} />}
            loading={actionBusy}
            size="small"
            type="primary"
            onClick={handleRefundAndClose}
          >
            {t("Refund and close")}
          </Button>
        ) : null}
        {platformCanOperate ? (
          <Button
            loading={actionBusy}
            size="small"
            type="tertiary"
            onClick={() => handleClose("platform")}
          >
            {t("Close ticket")}
          </Button>
        ) : null}
      </div>
    ) : null;

  const userActions =
    ticket && viewerRole === "user" && ticket.status !== "closed" ? (
      <div className="ticket-sheet-footer">
        <Button
          loading={actionBusy}
          size="small"
          type="tertiary"
          onClick={() => handleClose("user")}
        >
          {t("Close ticket")}
        </Button>
      </div>
    ) : null;

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      className="ticket-detail-sheet"
      onCancel={onClose}
      placement="right"
      title={headerArea ?? t("Ticket detail")}
      visible={Boolean(ticketNo)}
      width={isMobile ? "100%" : 620}
    >
      {loading || !ticket ? (
        <div className="flex h-64 items-center justify-center">
          <Spin size="large" />
        </div>
      ) : (
        <div className="flex h-full min-h-0 flex-col">
          {orderCard || resolutionCard ? (
            <div className="shrink-0 space-y-2 px-5 pt-4">
              {orderCard}
              {resolutionCard}
            </div>
          ) : null}
          <TicketConversation
            key={ticket.ticketNo}
            onReplied={applyUpdate}
            replyEnabled={viewerRole === "user" || platformCanOperate}
            ticket={ticket}
            viewerRole={viewerRole}
          />
          {platformActions}
          {userActions}
        </div>
      )}
    </SideSheet>
  );
}
