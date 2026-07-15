import { useEffect, useRef, useState } from "react";
import { Avatar, Button, Image, TextArea, Toast } from "@douyinfe/semi-ui";
import { ImagePlus, Send, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useAuth } from "@/context/auth-provider";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { formatTicketDateTime } from "./ticket-meta";
import {
  replyTicket,
  type MockTicket,
  type MockTicketMessage,
} from "./tickets-mock";

const AVATAR_COLORS = [
  "amber",
  "blue",
  "cyan",
  "green",
  "indigo",
  "light-blue",
  "light-green",
  "lime",
  "orange",
  "pink",
  "purple",
  "red",
  "teal",
  "violet",
  "yellow",
] as const;

function hashStr(value: string) {
  let hash = 0;
  for (const char of value) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return hash;
}

function avatarColor(seed: number) {
  return AVATAR_COLORS[Math.abs(seed) % AVATAR_COLORS.length];
}

function initialOf(name?: string | null, email?: string | null) {
  const source = name?.trim() || email?.trim() || "?";
  return source[0]?.toUpperCase() ?? "?";
}

function isImageAttachment(value: string) {
  return (
    /^(blob:|data:image)/.test(value) ||
    /^https?:\/\/.*\.(png|jpe?g|gif|webp|bmp|svg)(?:[?#].*)?$/i.test(value)
  );
}

interface PendingImage {
  id: string;
  url: string;
}

function readImageAsDataUrl(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () =>
      typeof reader.result === "string"
        ? resolve(reader.result)
        : reject(new Error("Unable to read image."));
    reader.onerror = () => reject(reader.error ?? new Error("Unable to read image."));
    reader.readAsDataURL(file);
  });
}

// Conversation thread + composer, shared by the user and admin detail sheets.
// Avatars and names always reflect the real account: the current logged-in
// account for the viewer's own messages, the ticket requester for user
// messages, and the platform agent name for platform messages.
export function TicketConversation({
  ticket,
  viewerRole,
  onReplied,
  sending,
  replyEnabled = true,
}: {
  ticket: MockTicket;
  viewerRole: "user" | "platform";
  onReplied: (next: MockTicket) => void;
  sending?: boolean;
  replyEnabled?: boolean;
}) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const [reply, setReply] = useState("");
  const [images, setImages] = useState<PendingImage[]>([]);
  const [posting, setPosting] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const imageSequenceRef = useRef(0);
  const imageReadIdRef = useRef(0);

  useEffect(
    () => () => {
      imageReadIdRef.current += 1;
    },
    []
  );

  useEffect(() => {
    const node = scrollRef.current;
    if (!node) return;
    node.scrollTop = node.scrollHeight;
  }, [ticket.messages.length]);

  const busy = posting || Boolean(sending);
  const canReply = replyEnabled && ticket.status !== "closed";
  const canSend = reply.trim().length > 0 || images.length > 0;

  const selfName =
    currentUser?.name || currentUser?.nickname || currentUser?.email || "";

  const identityFor = (message: MockTicketMessage) => {
    const isCurrentSender =
      message.senderUserId !== undefined &&
      message.senderUserId === currentUser?.id;
    if (isCurrentSender) {
      return {
        name: selfName || message.senderName,
        email: message.senderEmail || currentUser?.email || null,
        seed: currentUser?.id ?? hashStr(message.senderName),
      };
    }
    if (message.senderType === "user") {
      return {
        name: message.senderName || ticket.requesterName,
        email: message.senderEmail || ticket.requesterEmail,
        seed: message.senderUserId ?? ticket.requesterUserId,
      };
    }
    return {
      name: message.senderName,
      email: message.senderEmail ?? null,
      seed: message.senderUserId ?? hashStr(message.senderName),
    };
  };

  const pickImages = async (fileList: FileList | null) => {
    if (!fileList) return;
    const readId = imageReadIdRef.current;
    const selected = Array.from(fileList)
      .filter((file) => file.type.startsWith("image/"))
      .slice(0, Math.max(0, 6 - images.length));
    try {
      const next = await Promise.all(
        selected.map(async (file) => {
          imageSequenceRef.current += 1;
          return {
            id: `${file.name}-${file.size}-${imageSequenceRef.current}`,
            url: await readImageAsDataUrl(file),
          };
        })
      );
      if (imageReadIdRef.current !== readId) return;
      setImages((current) => [...current, ...next].slice(0, 6));
    } catch {
      if (imageReadIdRef.current === readId) {
        Toast.error(t("Ticket operation failed."));
      }
    } finally {
      if (fileRef.current) fileRef.current.value = "";
    }
  };

  const sendReply = async () => {
    if (!canReply || !canSend || busy) return;
    const content = reply.trim();
    const attachments = images.map((image) => image.url);
    setPosting(true);
    try {
      const next = await replyTicket(
        ticket.ticketNo,
        content || t("Sent an image"),
        viewerRole,
        attachments.length ? attachments : undefined,
        {
          userId: currentUser?.id,
          name: selfName,
          email: currentUser?.email,
        }
      );
      setReply("");
      imageReadIdRef.current += 1;
      setImages([]);
      onReplied(next);
    } catch {
      Toast.error(t("Ticket operation failed."));
    } finally {
      setPosting(false);
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="ticket-chat" ref={scrollRef}>
        {ticket.messages.map((message) => {
          if (message.senderType === "system") {
            return (
              <div className="ticket-chat-system" key={message.id}>
                <span className="min-w-0">{message.content}</span>
                <span className="ticket-chat-system-time">
                  {formatTicketDateTime(message.createdAt)}
                </span>
              </div>
            );
          }

          const isSelf = message.senderType === viewerRole;
          const identity = identityFor(message);
          const images = (message.attachments ?? []).filter(isImageAttachment);
          const files = (message.attachments ?? []).filter(
            (item) => !isImageAttachment(item)
          );

          return (
            <div
              className={`ticket-chat-row ${isSelf ? "is-self" : ""}`}
              key={message.id}
            >
              <Avatar
                className="shrink-0"
                color={avatarColor(identity.seed)}
                size="small"
              >
                {initialOf(identity.name, identity.email)}
              </Avatar>
              <div className="ticket-chat-main">
                <div className="ticket-chat-meta">
                  <span className="ticket-chat-name">{identity.name}</span>
                  {viewerRole === "platform" &&
                  message.senderType === "user" ? (
                    <>
                      {identity.email ? (
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={identity.email}
                        />
                      ) : null}
                      {ticket.requesterGroupName ? (
                        <span className="text-xs text-[var(--semi-color-text-2)]">
                          {ticket.requesterGroupName}
                        </span>
                      ) : null}
                    </>
                  ) : null}
                  <span className="ticket-chat-time">
                    {formatTicketDateTime(message.createdAt)}
                  </span>
                </div>
                <div className={`ticket-chat-bubble is-${message.senderType}`}>
                  {message.content}
                  {images.length > 0 ? (
                    <div className="mt-1.5 flex flex-wrap gap-1.5">
                      {images.map((src, index) => (
                        <Image
                          alt=""
                          height={84}
                          key={index}
                          src={src}
                          style={{ borderRadius: 8, objectFit: "cover" }}
                          width={84}
                        />
                      ))}
                    </div>
                  ) : null}
                  {files.length > 0 ? (
                    <div className="ticket-chat-attachments">
                      {files.map((name, index) => (
                        <span className="ticket-chat-attachment" key={index}>
                          <span className="truncate">{name}</span>
                        </span>
                      ))}
                    </div>
                  ) : null}
                </div>
              </div>
            </div>
          );
        })}
      </div>

      {canReply ? (
        <div className="ticket-composer">
          <div className="ticket-composer-identity">
            <Avatar
              color={avatarColor(currentUser?.id ?? 0)}
              size="extra-small"
            >
              {initialOf(selfName, currentUser?.email)}
            </Avatar>
            <span className="text-xs text-[var(--semi-color-text-2)]">
              {selfName || t("Reply")}
            </span>
          </div>

          {images.length > 0 ? (
            <div className="mb-2 flex flex-wrap gap-2">
              {images.map((image) => (
                <div className="relative" key={image.id}>
                  <img
                    alt=""
                    className="h-16 w-16 rounded-lg object-cover"
                    src={image.url}
                  />
                  <button
                    className="absolute -right-1.5 -top-1.5 flex h-4 w-4 items-center justify-center rounded-full bg-[var(--semi-color-danger)] text-white"
                    onClick={() =>
                      setImages((current) =>
                        current.filter((item) => item.id !== image.id)
                      )
                    }
                    type="button"
                  >
                    <X size={11} />
                  </button>
                </div>
              ))}
            </div>
          ) : null}

          <div className="flex items-end gap-2">
            <input
              accept="image/*"
              className="hidden"
              multiple
              onChange={(event) => void pickImages(event.target.files)}
              ref={fileRef}
              type="file"
            />
            <Button
              icon={<ImagePlus size={16} />}
              onClick={() => fileRef.current?.click()}
              theme="borderless"
              type="tertiary"
              title={t("Add image")}
            />
            <TextArea
              autosize={{ minRows: 1, maxRows: 4 }}
              maxCount={500}
              placeholder={t("Reply placeholder")}
              value={reply}
              onChange={(value) => setReply(String(value))}
              onKeyDown={(event) => {
                if (event.key === "Enter" && !event.shiftKey) {
                  event.preventDefault();
                  void sendReply();
                }
              }}
            />
            <Button
              disabled={!canSend}
              icon={<Send size={14} />}
              loading={busy}
              type="primary"
              onClick={() => void sendReply()}
            >
              {t("Send")}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
