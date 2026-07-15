import { useEffect, useMemo, useState } from "react";
import { Button, Empty, SideSheet, Spin, Table, Tabs } from "@douyinfe/semi-ui";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { useTranslation } from "react-i18next";

import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  listMockFinanceCardKeyRedemptions,
  type FinanceCardKey,
  type FinanceCardKeyRedemption,
} from "./admin-finance-mock";
import { CardKeyAccountCell } from "./card-key-meta";
import {
  DRAWER_PANEL_HEIGHT,
  DRAWER_TABLE_SCROLL_Y,
  formatDateTime,
  formatMoney,
  InfoItem,
  renderCardKeyStatusTag,
} from "./finance-meta";

export function CardKeyDetailSheet({
  card,
  onClose,
  onEdit,
  onToggleStatus,
  toggleLoading,
}: {
  card: FinanceCardKey | null;
  onClose: () => void;
  onEdit?: (card: FinanceCardKey) => void;
  onToggleStatus?: (card: FinanceCardKey) => void;
  toggleLoading?: boolean;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [activeTab, setActiveTab] = useState("basic");
  const [redemptions, setRedemptions] = useState<FinanceCardKeyRedemption[]>([]);
  const [loading, setLoading] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);

  useEffect(() => {
    setActiveTab("basic");
    setPage(1);
  }, [card?.key]);

  useEffect(() => {
    if (!card) {
      setRedemptions([]);
      return;
    }
    let cancelled = false;
    setLoading(true);
    void listMockFinanceCardKeyRedemptions(card.key)
      .then((items) => {
        if (!cancelled) setRedemptions(items);
      })
      .catch((error) => {
        if (!cancelled) {
          setRedemptions([]);
          console.error(getIamErrorMessage(t, error, "Operation failed."));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [card, t]);

  const pagedRedemptions = useMemo(() => {
    const start = (page - 1) * pageSize;
    return redemptions.slice(start, start + pageSize);
  }, [redemptions, page, pageSize]);

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onClose}
      placement="right"
      title={t("Card key details")}
      visible={Boolean(card)}
      width={isMobile ? "100%" : 940}
    >
      {card ? (
        <div className="flex min-h-full flex-col">
          <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
            <Tabs
              activeKey={activeTab}
              collapsible
              onChange={setActiveTab}
              type="line"
            >
              <Tabs.TabPane itemKey="basic" tab={t("Basic info")} />
              <Tabs.TabPane
                itemKey="redemptions"
                tab={t("Redemption records")}
              />
            </Tabs>
          </div>

          <div className="flex-1 p-5">
            {activeTab === "basic" ? (
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                <InfoItem
                  label={t("Card key")}
                  value={
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={card.key}
                    />
                  }
                />
                <InfoItem
                  label={t("Amount")}
                  value={
                    <span className="font-mono-data">
                      ¥{formatMoney(card.amount)}
                    </span>
                  }
                />
                <InfoItem
                  label={t("Status")}
                  value={renderCardKeyStatusTag(card.status, t)}
                />
                <InfoItem
                  label={t("Redemptions")}
                  value={
                    <span className="font-mono-data">
                      {card.redeemedCount}/{card.maxRedemptions}
                    </span>
                  }
                />
                <InfoItem
                  label={t("Expire at")}
                  value={formatDateTime(card.expireAt)}
                />
                <InfoItem
                  label={t("Created at")}
                  value={formatDateTime(card.createdAt)}
                />
                <div className="sm:col-span-2 lg:col-span-3">
                  <InfoItem
                    label={t("Owner")}
                    value={
                      <CardKeyAccountCell
                        email={card.ownerEmail}
                        groupName={card.ownerGroupName}
                        nickname={card.ownerNickname}
                        role={card.ownerRole}
                        t={t}
                        userId={card.ownerUserId}
                      />
                    }
                  />
                </div>
              </div>
            ) : null}

            {activeTab === "redemptions" ? (
              <div className="flex flex-col" style={{ height: DRAWER_PANEL_HEIGHT }}>
                <div className="min-h-0 flex-1 overflow-hidden">
                  {loading && redemptions.length === 0 ? (
                    <div className="flex h-full items-center justify-center">
                      <Spin size="large" />
                    </div>
                  ) : redemptions.length === 0 ? (
                    <Empty
                      darkModeImage={
                        <IllustrationNoResultDark
                          style={{ height: 150, width: 150 }}
                        />
                      }
                      description={t("No redemption records")}
                      image={
                        <IllustrationNoResult
                          style={{ height: 150, width: 150 }}
                        />
                      }
                      style={{ padding: 24 }}
                    />
                  ) : (
                    <Table
                      columns={[
                        {
                          title: t("User"),
                          dataIndex: "userEmail",
                          render: (
                            _: unknown,
                            record: FinanceCardKeyRedemption
                          ) => (
                            <CardKeyAccountCell
                              email={record.userEmail}
                              groupName={record.userGroupName}
                              nickname={record.userNickname}
                              role={record.userRole}
                              t={t}
                              userId={record.userId}
                            />
                          ),
                        },
                        {
                          title: t("Amount"),
                          dataIndex: "amount",
                          width: 140,
                          render: (value: string) => (
                            <span className="font-mono-data">
                              ¥{formatMoney(value)}
                            </span>
                          ),
                        },
                        {
                          title: t("Redeemed at"),
                          dataIndex: "redeemedAt",
                          width: 200,
                          render: (value: string) => formatDateTime(value),
                        },
                      ]}
                      dataSource={pagedRedemptions}
                      loading={loading}
                      pagination={false}
                      rowKey="id"
                      scroll={{ x: "max(100%, 480px)", y: DRAWER_TABLE_SCROLL_Y }}
                      size="small"
                    />
                  )}
                </div>
                {redemptions.length > 0 ? (
                  <div className="mt-3 flex flex-wrap items-center justify-end gap-3 border-t border-[var(--semi-color-border)] pt-3">
                    {createCardProPagination({
                      currentPage: page,
                      isMobile,
                      onPageChange: setPage,
                      onPageSizeChange: (size) => {
                        setPageSize(size);
                        setPage(1);
                      },
                      pageSize,
                      pageSizeOpts: [10, 20, 50, 100],
                      total: redemptions.length,
                      t,
                    })}
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>

          <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
            <Button
              loading={toggleLoading}
              onClick={() => onToggleStatus?.(card)}
              type="tertiary"
            >
              {card.status === "enabled" ? t("Disable") : t("Enable")}
            </Button>
            <Button onClick={() => onEdit?.(card)} type="primary">
              {t("Edit")}
            </Button>
          </div>
        </div>
      ) : null}
    </SideSheet>
  );
}
