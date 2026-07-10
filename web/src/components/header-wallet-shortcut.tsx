import { Link } from "@tanstack/react-router";
import { CirclePlus, Wallet } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type { CurrentUser } from "@/context/auth-provider";
import { cn } from "@/lib/utils";
import { getWallet } from "@/lib/wallet-api";
import { subscribeWalletUpdated } from "@/lib/wallet-events";

function formatBalance(value: string | null) {
  if (value == null) return "￥-";
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return "￥-";
  return `￥${numeric.toLocaleString("zh-CN", {
    maximumFractionDigits: 3,
    minimumFractionDigits: 2,
  })}`;
}

function formatUserGroup(
  group: CurrentUser["userGroup"] | null | undefined,
  t: (key: string) => string
) {
  if (!group) return "-";
  if (group.code === "normal") return t("Normal User Group");
  return group.name || group.code || "-";
}

export function HeaderWalletShortcut({
  userGroup,
}: {
  userGroup: CurrentUser["userGroup"];
}) {
  const { t } = useTranslation();
  const [balance, setBalance] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const groupLabel = formatUserGroup(userGroup, t);

  useEffect(() => {
    let cancelled = false;

    const refreshWallet = () => {
      setLoading(true);
      void getWallet()
        .then((wallet) => {
          if (!cancelled) setBalance(wallet.consumerBalance);
        })
        .catch(() => {
          if (!cancelled) setBalance(null);
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    };

    refreshWallet();
    const unsubscribe = subscribeWalletUpdated(refreshWallet);
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, []);

  return (
    <div className="hidden h-8 items-center rounded-full bg-surface-sunken px-1 shadow-none lg:flex">
      <div
        aria-label={t("User Group")}
        className="flex h-7 max-w-[112px] items-center rounded-full px-2 text-xs font-medium text-muted-foreground"
      >
        <span className="truncate">{groupLabel}</span>
      </div>
      <span aria-hidden="true" className="mx-0.5 h-4 w-px bg-border" />
      <Link
        to="/wallet"
        aria-label={t("Wallet Management")}
        className={cn(
          "flex h-7 min-w-[92px] items-center gap-1.5 rounded-full px-2 text-xs font-medium text-muted-foreground transition-colors",
          "hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        )}
      >
        <Wallet className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="tabular-nums">{loading ? "..." : formatBalance(balance)}</span>
      </Link>
      <Link
        to="/wallet"
        className={cn(
          "flex h-7 w-7 items-center justify-center rounded-full text-muted-foreground transition-colors",
          "hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        )}
        aria-label={t("Recharge")}
        title={t("Recharge")}
      >
        <CirclePlus aria-hidden="true" className="size-4" />
      </Link>
    </div>
  );
}
