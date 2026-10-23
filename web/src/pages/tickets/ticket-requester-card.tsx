import { useEffect, useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { hasPermissionKey, useAuth } from "@/context/auth-provider";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  getAdminUserWallet,
  type AdminWallet,
} from "../admin-users/admin-users-api";
import { formatTicketAmount } from "./ticket-meta";
import type { Ticket } from "./tickets-api";

export function TicketRequesterCard({ ticket }: { ticket: Ticket }) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const canReadWallet = hasPermissionKey(currentUser, "billing:wallet:read");
  const [wallet, setWallet] = useState<AdminWallet | null>(null);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    setWallet(null);
    setError("");
    if (!canReadWallet) return;
    let cancelled = false;
    void getAdminUserWallet(ticket.requesterUserId)
      .then((next) => {
        if (!cancelled) setWallet(next);
      })
      .catch((err) => {
        if (!cancelled) setError(getIamErrorMessage(t, err, "Wallet load failed."));
      });
    return () => {
      cancelled = true;
    };
  }, [canReadWallet, ticket.requesterUserId, ticket.updatedAt, reload, t]);

  return (
    <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3 text-xs">
      <dl className="grid min-w-0 grid-cols-2 gap-x-4 gap-y-2">
        {[
          ["User ID", String(ticket.requesterUserId)],
          ["Username", ticket.requesterName],
          ["Email", ticket.requesterEmail],
        ].map(([label, value]) => (
          <div
            className={`min-w-0 ${label === "Email" ? "col-span-2" : ""}`}
            key={label}
          >
            <dt className="mb-0.5 text-[var(--semi-color-text-2)]">{t(label)}</dt>
            <dd className="m-0 min-w-0">
              {value ? (
                <CopyableTableText copiedText={t("Copied")} text={value} />
              ) : "—"}
            </dd>
          </div>
        ))}
        {canReadWallet ? (
          <>
            <div>
              <dt className="mb-0.5 text-[var(--semi-color-text-2)]">
                {t("Wallet points")}
              </dt>
              <dd className="m-0 font-mono-data font-semibold">
                {wallet ? formatTicketAmount(wallet.consumerBalance) : "—"}
              </dd>
            </div>
            <div>
              <dt className="mb-0.5 text-[var(--semi-color-text-2)]">
                {t("Withdrawable points")}
              </dt>
              <dd className="m-0 font-mono-data font-semibold">
                {wallet ? formatTicketAmount(wallet.supplierAvailable) : "—"}
              </dd>
            </div>
          </>
        ) : null}
      </dl>
      {canReadWallet && !wallet ? (
        <div
          className="mt-2 text-[var(--semi-color-text-2)]"
          role={error ? "alert" : "status"}
        >
          {error || t("Loading...")}
          {error ? (
            <Button
              onClick={() => setReload((value) => value + 1)}
              size="small"
              theme="borderless"
            >
              {t("Retry")}
            </Button>
          ) : null}
        </div>
      ) : null}
      {ticket.withdrawalAmount ? (
        <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-[var(--semi-color-border)] pt-2">
          <span>{t("Requested withdrawal points")}</span>
          <strong className="font-mono-data text-sm text-[var(--semi-color-primary)]">
            {formatTicketAmount(ticket.withdrawalAmount)}
          </strong>
        </div>
      ) : null}
    </div>
  );
}
