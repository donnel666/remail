import { describe, expect, it } from "vitest";

import { buildOrderRefFromOrder } from "../orders/ticket-order-handoff";
import {
  createTicket,
  getTicket,
  markTicketRead,
  replyTicket,
} from "./tickets-mock";

describe("ticket mock runtime state", () => {
  it("tracks requester and platform unread state independently", async () => {
    const created = await createTicket({
      ticketType: "general",
      title: "Unread state test",
      firstMessage: "Please check this ticket.",
    });
    expect(created.requesterUnreadCount).toBe(0);
    expect(created.platformUnreadCount).toBe(1);

    await markTicketRead(created.ticketNo, "platform");
    expect((await getTicket(created.ticketNo)).platformUnreadCount).toBe(0);

    const platformReply = await replyTicket(
      created.ticketNo,
      "Handled by the current admin.",
      "platform",
      undefined,
      { userId: 9, name: "Admin Alice", email: "alice@example.com" }
    );
    expect(platformReply.platformUnreadCount).toBe(0);
    expect(platformReply.requesterUnreadCount).toBe(1);
    expect(
      platformReply.messages[platformReply.messages.length - 1]
    ).toMatchObject({
      senderName: "Admin Alice",
      senderUserId: 9,
      senderEmail: "alice@example.com",
    });

    await markTicketRead(created.ticketNo, "user");
    const userReply = await replyTicket(
      created.ticketNo,
      "Thanks.",
      "user",
      undefined,
      { userId: 1001, name: "Requester", email: "me@example.com" }
    );
    expect(userReply.requesterUnreadCount).toBe(0);
    expect(userReply.platformUnreadCount).toBe(1);
  });

  it("keeps the actual project logo in the order handoff contract", () => {
    const order = buildOrderRefFromOrder({
      orderNo: "OR-LOGO-1",
      projectName: "Telegram",
      projectLogoUrl: "https://cdn.example.com/telegram.png",
      deliveryEmail: "mail@example.com",
      payAmount: "1.25",
      serviceMode: "code",
      supplyPolicy: "public_only",
    });

    expect(order.projectLogoUrl).toBe(
      "https://cdn.example.com/telegram.png"
    );
  });
});
