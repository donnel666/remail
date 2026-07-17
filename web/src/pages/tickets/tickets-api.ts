// Real /v1/tickets client for the after-sales ticket pages. Mirrors the shape
// of the former tickets-mock so the pages and drawer components only swap their
// import. Domain types use the DTOs from the generated OpenAPI schema, mapped
// into the console-friendly shapes the UI already consumes.

import type { components } from "@/lib/openapi/schema";
import { apiClient, csrfHeader, unwrap } from "@/lib/api-client";
import { generateIdempotencyKey } from "@/lib/idempotency";

import type {
  TicketOrderRef,
  TicketOrderSource as HandoffTicketOrderSource,
} from "../orders/ticket-order-handoff";

type TicketResponse = components["schemas"]["TicketResponse"];
type TicketListResponseDTO = components["schemas"]["TicketListResponse"];
type TicketMessageDTO = components["schemas"]["TicketMessageResponse"];
type TicketOrderDTO = components["schemas"]["TicketOrderResponse"];
type TicketFacetsDTO = components["schemas"]["TicketFacets"];

export type TicketSender = "user" | "platform" | "system";
export type TicketStatus = "open" | "processing" | "closed";
export type TicketType = "order" | "general";
export type TicketResolutionKind = "refunded" | "closed";

export interface TicketMessage {
  id: number;
  senderType: TicketSender;
  senderName: string;
  senderUserId?: number;
  senderEmail?: string;
  content: string;
  createdAt: string;
  attachments?: string[];
}

export interface TicketResolution {
  kind: TicketResolutionKind;
  note?: string;
  refundAmount?: number;
}

export interface Ticket {
  id: number;
  ticketNo: string;
  ticketType: TicketType;
  title: string;
  status: TicketStatus;
  order?: TicketOrderRef;
  requesterUserId: number;
  requesterEmail: string;
  requesterName: string;
  requesterRole: string;
  requesterGroupName: string;
  resolution?: TicketResolution;
  requesterUnreadCount: number;
  platformUnreadCount: number;
  messages: TicketMessage[];
  createdAt: string;
  updatedAt: string;
}

export interface TicketListFilter {
  search?: string;
  ticketType?: TicketType;
  status?: TicketStatus;
  createdFrom?: string;
  createdTo?: string;
}

export interface TicketFacets {
  ticketType: Record<"all" | TicketType, number>;
  status: Record<"all" | TicketStatus, number>;
}

export interface TicketListResponse {
  items: Ticket[];
  total: number;
  facets: TicketFacets;
  nextAfterId?: number;
}

// ---------------------------------------------------------------------------
// DTO → view mappers
// ---------------------------------------------------------------------------

function toOrderRef(order: TicketOrderDTO): TicketOrderRef {
  const payAmount = Number(order.payAmount);
  return {
    orderNo: order.orderNo,
    projectName: order.projectName || "-",
    projectLogoUrl: order.projectLogoUrl || undefined,
    deliveryEmail: order.deliveryEmail,
    payAmount: Number.isFinite(payAmount) ? payAmount : 0,
    serviceMode: order.serviceMode,
    afterSaleUntil: order.afterSaleUntil || undefined,
    hasSupplier: order.hasSupplier ?? false,
  };
}

function toMessage(message: TicketMessageDTO): TicketMessage {
  return {
    id: message.id,
    senderType: message.senderType,
    senderName: message.senderName ?? "",
    senderUserId: message.senderUserId,
    senderEmail: message.senderEmail,
    content: message.content,
    createdAt: message.createdAt,
    attachments: message.attachments,
  };
}

function toTicket(dto: TicketResponse): Ticket {
  return {
    id: dto.id,
    ticketNo: dto.ticketNo,
    ticketType: dto.ticketType,
    title: dto.title,
    status: dto.status,
    order: dto.order ? toOrderRef(dto.order) : undefined,
    requesterUserId: dto.requesterUserId,
    requesterEmail: dto.requesterEmail,
    requesterName: dto.requesterName,
    requesterRole: dto.requesterRole,
    requesterGroupName: dto.requesterGroupName,
    resolution: dto.resolution
      ? {
          kind: dto.resolution.kind,
          refundAmount:
            dto.resolution.refundAmount != null
              ? Number(dto.resolution.refundAmount)
              : undefined,
        }
      : undefined,
    requesterUnreadCount: dto.requesterUnreadCount,
    platformUnreadCount: dto.platformUnreadCount,
    messages: (dto.messages ?? []).map(toMessage),
    createdAt: dto.createdAt,
    updatedAt: dto.updatedAt,
  };
}

function toFacets(facets: TicketFacetsDTO | undefined): TicketFacets {
  return {
    ticketType: {
      all: facets?.ticketType.all ?? 0,
      order: facets?.ticketType.order ?? 0,
      general: facets?.ticketType.general ?? 0,
    },
    status: {
      all: facets?.status.all ?? 0,
      open: facets?.status.open ?? 0,
      processing: facets?.status.processing ?? 0,
      closed: facets?.status.closed ?? 0,
    },
  };
}

function toListResponse(response: TicketListResponseDTO): TicketListResponse {
  return {
    items: response.items.map(toTicket),
    total: response.total,
    facets: toFacets(response.facets),
    nextAfterId: response.nextAfterId ?? undefined,
  };
}

function listQuery(filter: TicketListFilter) {
  return {
    ticketType: filter.ticketType,
    status: filter.status,
    search: filter.search,
    createdFrom: filter.createdFrom,
    createdTo: filter.createdTo,
  };
}

// ---------------------------------------------------------------------------
// User-facing API (submitter perspective)
// ---------------------------------------------------------------------------

export async function listMyTickets(
  filter: TicketListFilter,
  offset: number,
  limit: number,
  afterId?: number
): Promise<TicketListResponse> {
  return toListResponse(
    await unwrap<TicketListResponseDTO>(
      await apiClient.GET("/v1/tickets", {
        params: { query: { ...listQuery(filter), offset, afterId, limit } },
      })
    )
  );
}

export async function getTicket(ticketNo: string): Promise<Ticket> {
  return toTicket(
    await unwrap<TicketResponse>(
      await apiClient.GET("/v1/tickets/{ticketNo}", {
        params: { path: { ticketNo } },
      })
    )
  );
}

export interface CreateTicketInput {
  ticketType: TicketType;
  title: string;
  firstMessage: string;
  order?: TicketOrderRef;
  attachments?: string[];
}

export async function createTicket(input: CreateTicketInput): Promise<Ticket> {
  return toTicket(
    await unwrap<TicketResponse>(
      await apiClient.POST("/v1/tickets", {
        body: {
          ticketType: input.ticketType,
          title: input.title,
          firstMessage: input.firstMessage,
          orderNo: input.order?.orderNo,
          attachments: input.attachments,
        },
        params: { header: csrfHeader() },
      })
    )
  );
}

export async function markTicketRead(
  ticketNo: string,
  viewerRole: "user" | "platform"
): Promise<void> {
  if (viewerRole === "user") {
    await unwrap(
      await apiClient.POST("/v1/tickets/{ticketNo}/read", {
        params: { path: { ticketNo }, header: csrfHeader() },
      })
    );
    return;
  }
  await unwrap(
    await apiClient.POST("/v1/admin/tickets/{ticketNo}/read", {
      params: { path: { ticketNo }, header: csrfHeader() },
    })
  );
}

// ---------------------------------------------------------------------------
// Admin / platform-facing API
// ---------------------------------------------------------------------------

export async function listAllTickets(
  filter: TicketListFilter,
  offset: number,
  limit: number,
  afterId?: number
): Promise<TicketListResponse> {
  return toListResponse(
    await unwrap<TicketListResponseDTO>(
      await apiClient.GET("/v1/admin/tickets", {
        params: { query: { ...listQuery(filter), offset, afterId, limit } },
      })
    )
  );
}

// Shared reply handler for both sides. The backend derives the sender from the
// session and route; `sender` is accepted only for call-site compatibility.
export async function replyTicket(
  ticketNo: string,
  content: string,
  senderType: "user" | "platform",
  attachments?: string[],
  _sender?: { userId?: number; name?: string; email?: string }
): Promise<Ticket> {
  const body = { content, attachments };
  if (senderType === "platform") {
    return toTicket(
      await unwrap<TicketResponse>(
        await apiClient.POST("/v1/admin/tickets/{ticketNo}/messages", {
          body,
          params: { path: { ticketNo }, header: csrfHeader() },
        })
      )
    );
  }
  return toTicket(
    await unwrap<TicketResponse>(
      await apiClient.POST("/v1/tickets/{ticketNo}/messages", {
        body,
        params: { path: { ticketNo }, header: csrfHeader() },
      })
    )
  );
}

export async function closeTicket(
  ticketNo: string,
  by: "user" | "platform" = "platform"
): Promise<Ticket> {
  if (by === "user") {
    return toTicket(
      await unwrap<TicketResponse>(
        await apiClient.POST("/v1/tickets/{ticketNo}/close", {
          params: { path: { ticketNo }, header: csrfHeader() },
        })
      )
    );
  }
  return toTicket(
    await unwrap<TicketResponse>(
      await apiClient.POST("/v1/admin/tickets/{ticketNo}/close", {
        params: { path: { ticketNo }, header: csrfHeader() },
      })
    )
  );
}

export async function refundAndCloseTicket(
  ticketNo: string,
  _amount?: number
): Promise<Ticket> {
  return toTicket(
    await unwrap<TicketResponse>(
      await apiClient.POST("/v1/admin/tickets/{ticketNo}/refund", {
        params: {
          path: { ticketNo },
          header: { ...csrfHeader(), "Idempotency-Key": generateIdempotencyKey() },
        },
      })
    )
  );
}

// ---------------------------------------------------------------------------
// Order → ticket handoff helpers (pure; consumed by the create flow)
// ---------------------------------------------------------------------------

export interface TicketOrderSource extends HandoffTicketOrderSource {
  status: string;
}

export type OrderAfterSaleState =
  | { eligible: true; deadline?: string }
  | { eligible: false; reason: "expired" | "not_delivered" | "refunded" };

export function getOrderAfterSaleState(
  order: TicketOrderSource
): OrderAfterSaleState {
  if (order.status === "refunded") return { eligible: false, reason: "refunded" };
  if (["pending_payment", "paid", "failed"].includes(order.status)) {
    return { eligible: false, reason: "not_delivered" };
  }
  const deadline = order.afterSaleUntil ?? order.receiveUntil ?? undefined;
  if (order.status === "active") {
    return { eligible: true, deadline };
  }
  if (deadline && new Date(deadline).getTime() > Date.now()) {
    return { eligible: true, deadline };
  }
  return { eligible: false, reason: "expired" };
}
