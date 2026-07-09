import { Empty, Input, Modal, Tag, Typography } from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { Mail } from "lucide-react";
import { useEffect, useMemo, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { cn } from "@/lib/utils";

import { FetchControl } from "./fetch-control";
import type { FetchSource, WorkbenchMessage } from "./types";
import { formatDateTime } from "./utils";

const { Text } = Typography;

function matchesMail(message: WorkbenchMessage, search: string) {
  const q = search.trim().toLowerCase();
  if (!q) return true;
  return [message.subject, message.body]
    .join(" ")
    .toLowerCase()
    .includes(q);
}

function messageStatusColor(status: WorkbenchMessage["status"]) {
  if (status === "matched") return "green" as const;
  if (status === "ignored") return "grey" as const;
  return "blue" as const;
}

function messageStatusLabel(status: WorkbenchMessage["status"], t: (key: string) => string) {
  if (status === "matched") return t("Matched");
  if (status === "ignored") return t("Ignored");
  return t("Received");
}

export function MailboxClient({
  email,
  messages,
  onFetch,
}: {
  email: string;
  messages: WorkbenchMessage[];
  onFetch: (source: FetchSource) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [search, setSearch] = useState("");
  const filteredMessages = useMemo(
    () => messages.filter((message) => matchesMail(message, search)),
    [messages, search]
  );
  const hasVerificationCode = messages.some((message) => message.verificationCode);
  const [selectedMessageId, setSelectedMessageId] = useState("");

  useEffect(() => {
    setSelectedMessageId(filteredMessages[0]?.id ?? "");
  }, [email, filteredMessages]);

  const selectedMessage =
    filteredMessages.find((message) => message.id === selectedMessageId) ??
    filteredMessages[0];

  function handleMessageKeyDown(
    event: KeyboardEvent<HTMLDivElement>,
    messageId: string
  ) {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    setSelectedMessageId(messageId);
  }

  return (
    <div className="mailbox-client">
      <aside className="mailbox-client-sidebar">
        <div className="mailbox-client-toolbar">
          <div className="mailbox-client-address font-mono-data">
            <OverflowTooltip content={email}>{email}</OverflowTooltip>
          </div>
          <FetchControl autoEnabled={!hasVerificationCode} compact onFetch={onFetch} />
        </div>
        <Input
          className="resources-search-input mailbox-client-search"
          onChange={(value) => setSearch(String(value))}
          placeholder={t("Search subject or body")}
          prefix={<IconSearch />}
          showClear
          value={search}
        />
        <div className="mailbox-client-list">
          {filteredMessages.length === 0 ? (
            <Empty description={t("No matched mail")} />
          ) : (
            filteredMessages.map((message) => (
              <div
                className={cn(
                  "mailbox-client-item",
                  selectedMessage?.id === message.id && "is-active"
                )}
                key={message.id}
                onClick={() => setSelectedMessageId(message.id)}
                onKeyDown={(event) => handleMessageKeyDown(event, message.id)}
                role="button"
                tabIndex={0}
              >
                <span className="mailbox-client-item-head">
                  <OverflowTooltip
                    className="mailbox-client-item-subject"
                    content={message.subject}
                  >
                    {message.subject}
                  </OverflowTooltip>
                  {message.verificationCode ? (
                    <span
                      className="mailbox-code-copy-wrap"
                      onClick={(event) => event.stopPropagation()}
                    >
                      <Text
                        className="mailbox-code-copy"
                        copyable={createCopyableConfig(
                          message.verificationCode,
                          t("Copied")
                        )}
                      >
                        {message.verificationCode}
                      </Text>
                    </span>
                  ) : (
                    <Tag
                      color={messageStatusColor(message.status)}
                      shape="circle"
                      size="small"
                    >
                      {messageStatusLabel(message.status, t)}
                    </Tag>
                  )}
                </span>
                <span className="mailbox-client-item-meta">
                  <OverflowTooltip content={message.sender}>
                    {message.sender}
                  </OverflowTooltip>
                  <span>{formatDateTime(message.receivedAt)}</span>
                </span>
                <OverflowTooltip
                  className="mailbox-client-item-preview"
                  content={message.preview}
                >
                  {message.preview}
                </OverflowTooltip>
              </div>
            ))
          )}
        </div>
      </aside>

      <main className="mailbox-client-detail">
        {selectedMessage ? (
          <>
            <div className="mailbox-client-detail-head">
              <div className="min-w-0">
                <OverflowTooltip
                  className="mailbox-client-subject"
                  content={selectedMessage.subject}
                >
                  {selectedMessage.subject}
                </OverflowTooltip>
                <OverflowTooltip
                  className="mailbox-client-sender"
                  content={`${selectedMessage.sender} · ${formatDateTime(
                    selectedMessage.receivedAt
                  )}`}
                >
                  {selectedMessage.sender} · {formatDateTime(selectedMessage.receivedAt)}
                </OverflowTooltip>
              </div>
              {selectedMessage.verificationCode ? (
                <div className="mailbox-detail-code">
                  <span>{t("Verification code")}</span>
                  <Text
                    className="font-mono-data"
                    copyable={createCopyableConfig(
                      selectedMessage.verificationCode,
                      t("Copied")
                    )}
                  >
                    {selectedMessage.verificationCode}
                  </Text>
                </div>
              ) : null}
            </div>
            <pre className="mailbox-client-body">{selectedMessage.body}</pre>
          </>
        ) : (
          <div className="mailbox-client-empty">
            <Mail size={36} />
            <Empty description={t("No selected mail")} />
          </div>
        )}
      </main>
    </div>
  );
}

export function MailboxClientModal({
  email,
  messages = [],
  onClose,
  onFetch,
}: {
  email?: string;
  messages?: WorkbenchMessage[];
  onClose: () => void;
  onFetch: (source: FetchSource) => void | Promise<void>;
}) {
  const open = Boolean(email);

  return (
    <Modal
      bodyStyle={{ height: "100%", minHeight: 0, overflow: "hidden", padding: 0 }}
      className="mailbox-client-modal"
      footer={null}
      height="min(720px, calc(100vh - 160px))"
      onCancel={onClose}
      style={{ margin: 0, maxWidth: "calc(100vw - 64px)" }}
      title={null}
      visible={open}
      width="min(1120px, calc(100vw - 64px))"
    >
      {email ? (
        <MailboxClient email={email} messages={messages} onFetch={onFetch} />
      ) : null}
    </Modal>
  );
}
