import { Button, Empty, Toast } from "@douyinfe/semi-ui";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { Mail } from "lucide-react";
import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { IamApiError } from "@/lib/api-client";
import {
  readPickupMail,
  readPickupMessage,
  type OrderMailResponse,
} from "@/lib/mailmatch-api";

import { MailboxClient } from "./workbench/mailbox-client";
import type { FetchSource, WorkbenchMessage } from "./workbench/types";

function toPickupMessages(items: OrderMailResponse["items"]): WorkbenchMessage[] {
  return items.map((item) => {
    return {
      body: "",
      id: String(item.id),
      preview: item.bodyPreview,
      receivedAt: item.receivedAt,
      recipient: item.recipient,
      sender: item.sender,
      status: item.verificationCode ? "matched" : "received",
      subject: item.subject || "(No subject)",
      verificationCode: item.verificationCode,
    };
  });
}

export default function Pickup() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const rawHash = typeof window === "undefined" ? "" : window.location.hash;
  const rawSearch = typeof window === "undefined" ? "" : window.location.search;
  const params = useMemo(
    () => {
      const searchParams = new URLSearchParams(rawSearch);
      if (searchParams.has("email") || searchParams.has("token")) {
        return searchParams;
      }
      return new URLSearchParams(rawHash.replace(/^#/, ""));
    },
    [rawHash, rawSearch, location.pathname]
  );
  const email = params.get("email")?.trim() ?? "";
  const token = params.get("token")?.trim() ?? "";
  const [messages, setMessages] = useState<WorkbenchMessage[]>([]);
  const loadSeqRef = useRef(0);

  async function loadPickup(source: FetchSource) {
    if (!email || !token) return;
    const seq = loadSeqRef.current + 1;
    loadSeqRef.current = seq;
    try {
      void source;
      const result = await readPickupMail(email, token);
      if (loadSeqRef.current !== seq) return;
      setMessages(toPickupMessages(result.items));
      if (result.fetch?.nextFetchAllowedAt) {
        return Math.max(
          1,
          Math.ceil(
            (Date.parse(result.fetch.nextFetchAllowedAt) - Date.now()) / 1000
          )
        );
      }
      return 5;
    } catch (err) {
      if (err instanceof IamApiError && err.status === 429) {
        return err.retryAfterSeconds;
      }
      if (source === "manual") {
        Toast.error(
          err instanceof Error ? err.message : t("An unexpected error occurred.")
        );
      }
      return 30;
    }
  }

  if (!email || !token) {
    return (
      <div className="pickup-empty-page">
        <Empty
          image={<Mail size={56} strokeWidth={1.5} />}
          title={t("Invalid pickup link")}
          description={t("Pickup link requires email and token.")}
        />
        <Button onClick={() => void navigate({ to: "/dashboard" })} type="primary">
          {t("Back to workbench")}
        </Button>
      </div>
    );
  }

  return (
    <div className="pickup-page">
      <MailboxClient
        email={email}
        fetchKey={`pickup:${token}`}
        messages={messages}
        onLoadMessage={async (messageId) => {
          const detail = await readPickupMessage(email, token, Number(messageId));
          return detail.body;
        }}
        onFetch={loadPickup}
      />
    </div>
  );
}
