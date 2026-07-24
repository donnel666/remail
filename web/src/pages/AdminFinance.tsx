import { useState, type ReactNode } from "react";
import { Tabs } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { hasPermission, useAuth } from "@/context/auth-provider";
import { BalancesPanel } from "./admin-finance/balances-panel";
import { CardKeysPanel } from "./admin-finance/card-keys-panel";
import { InvitesPanel } from "./admin-finance/invites-panel";
import { OverviewPanel } from "./admin-finance/overview-panel";
import { TransactionsPanel } from "./admin-finance/transactions-panel";

type FinanceTab =
  | "overview"
  | "invites"
  | "cards"
  | "transactions"
  | "balances";

export default function AdminFinance() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const [activeTab, setActiveTab] = useState<FinanceTab>("overview");
  const canReadInvites = hasPermission(currentUser, "iam:invite", "read");
  const canReadCards = hasPermission(currentUser, "billing:card", "read");
  const effectiveTab =
    (activeTab === "invites" && !canReadInvites) ||
    (activeTab === "cards" && !canReadCards)
      ? "overview"
      : activeTab;

  const tabsArea = (
    <Tabs
      activeKey={effectiveTab}
      className="mb-2"
      collapsible
      onChange={(key) => setActiveTab(key as FinanceTab)}
      type="card"
    >
      <Tabs.TabPane itemKey="overview" tab={t("Overview")} />
      {canReadInvites ? (
        <Tabs.TabPane itemKey="invites" tab={t("Invite codes")} />
      ) : null}
      {canReadCards ? (
        <Tabs.TabPane itemKey="cards" tab={t("Card keys")} />
      ) : null}
      <Tabs.TabPane itemKey="transactions" tab={t("Transactions")} />
      <Tabs.TabPane itemKey="balances" tab={t("User balances")} />
    </Tabs>
  );

  let panel: ReactNode = null;
  if (effectiveTab === "overview") {
    panel = <OverviewPanel tabsArea={tabsArea} />;
  } else if (effectiveTab === "invites") {
    panel = <InvitesPanel tabsArea={tabsArea} />;
  } else if (effectiveTab === "cards") {
    panel = <CardKeysPanel tabsArea={tabsArea} />;
  } else if (effectiveTab === "transactions") {
    panel = <TransactionsPanel tabsArea={tabsArea} />;
  } else {
    panel = <BalancesPanel tabsArea={tabsArea} />;
  }

  if (effectiveTab === "overview") return panel;

  return <div className="console-content-width py-5">{panel}</div>;
}
