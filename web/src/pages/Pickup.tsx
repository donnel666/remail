import { Button, Empty, Toast } from "@douyinfe/semi-ui";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { Mail } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { MailboxClient } from "./workbench/mailbox-client";

export default function Pickup() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const rawHash = typeof window === "undefined" ? "" : window.location.hash;
  const params = useMemo(
    () => new URLSearchParams(rawHash.replace(/^#/, "")),
    [rawHash, location.pathname]
  );
  const email = params.get("email")?.trim() ?? "";
  const token = params.get("token")?.trim() ?? "";

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
        messages={[]}
        onFetch={() => {
          Toast.info(t("Feature is not implemented yet."));
        }}
      />
    </div>
  );
}
