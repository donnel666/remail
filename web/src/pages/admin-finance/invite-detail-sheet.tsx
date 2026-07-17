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
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  listFinanceInviteUses,
  type FinanceInvite,
  type FinanceInviteUse,
} from "./admin-finance-api";
import {
  DRAWER_PANEL_HEIGHT,
  DRAWER_TABLE_SCROLL_Y,
  formatDateTime,
  InfoItem,
  renderEnabledTag,
} from "./finance-meta";
import { InviteAccountCell } from "./invite-meta";

export function InviteDetailSheet({
  invite,
  onClose,
  onEdit,
  onToggleEnabled,
  toggleLoading,
}: {
  invite: FinanceInvite | null;
  onClose: () => void;
  onEdit?: (invite: FinanceInvite) => void;
  onToggleEnabled?: (invite: FinanceInvite) => void;
  toggleLoading?: boolean;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [activeTab, setActiveTab] = useState("basic");
  const [uses, setUses] = useState<FinanceInviteUse[]>([]);
  const [loading, setLoading] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setPage(1), [pageSize]);

  useEffect(() => {
    setActiveTab("basic");
    setPage(1);
  }, [invite?.code]);

  useEffect(() => {
    if (!invite) {
      setUses([]);
      return;
    }
    let cancelled = false;
    setLoading(true);
    void listFinanceInviteUses(invite.code)
      .then((items) => {
        if (!cancelled) setUses(items);
      })
      .catch((error) => {
        if (!cancelled) {
          setUses([]);
          console.error(getIamErrorMessage(t, error, "Operation failed."));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [invite, t]);

  const pagedUses = useMemo(() => {
    const start = (page - 1) * pageSize;
    return uses.slice(start, start + pageSize);
  }, [uses, page, pageSize]);

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onClose}
      placement="right"
      title={t("Invite code details")}
      visible={Boolean(invite)}
      width={isMobile ? "100%" : 940}
    >
      {invite ? (
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
                itemKey="usage"
                tab={t("Invite usage records")}
              />
            </Tabs>
          </div>

          <div className="flex-1 p-5">
            {activeTab === "basic" ? (
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                <InfoItem
                  label={t("Invite code")}
                  value={
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={invite.code}
                    />
                  }
                />
                <InfoItem
                  label={t("Status")}
                  value={renderEnabledTag(invite.enabled, t)}
                />
                <InfoItem
                  label={t("Usage")}
                  value={
                    <span className="font-mono-data">
                      {invite.used}/
                      {invite.maxUse >= 2147483647 ? "∞" : invite.maxUse}
                    </span>
                  }
                />
                <InfoItem
                  label={t("Expire at")}
                  value={formatDateTime(invite.expireAt)}
                />
                <InfoItem
                  label={t("Created at")}
                  value={formatDateTime(invite.createdAt)}
                />
                <div className="sm:col-span-2 lg:col-span-3">
                  <InfoItem
                    label={t("Owner")}
                    value={
                      <InviteAccountCell
                        email={invite.ownerEmail}
                        groupName={invite.ownerGroupName}
                        nickname={invite.ownerNickname}
                        role={invite.ownerRole}
                        t={t}
                        userId={invite.ownerUserId}
                      />
                    }
                  />
                </div>
              </div>
            ) : null}

            {activeTab === "usage" ? (
              <div className="flex flex-col" style={{ height: DRAWER_PANEL_HEIGHT }}>
                <div className="min-h-0 flex-1 overflow-hidden">
                  {loading && uses.length === 0 ? (
                    <div className="flex h-full items-center justify-center">
                      <Spin size="large" />
                    </div>
                  ) : uses.length === 0 ? (
                    <Empty
                      darkModeImage={
                        <IllustrationNoResultDark
                          style={{ height: 150, width: 150 }}
                        />
                      }
                      description={t("No invite usage records")}
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
                          render: (_: unknown, record: FinanceInviteUse) => (
                            <InviteAccountCell
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
                          title: t("Used at"),
                          dataIndex: "usedAt",
                          width: 200,
                          render: (value: string) => formatDateTime(value),
                        },
                      ]}
                      dataSource={pagedUses}
                      loading={loading}
                      pagination={false}
                      rowKey="id"
                      scroll={{ x: "max(100%, 480px)", y: DRAWER_TABLE_SCROLL_Y }}
                      size="small"
                    />
                  )}
                </div>
                {uses.length > 0 ? (
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
                      total: uses.length,
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
              onClick={() => onToggleEnabled?.(invite)}
              type="tertiary"
            >
              {invite.enabled ? t("Disable") : t("Enable")}
            </Button>
            <Button onClick={() => onEdit?.(invite)} type="primary">
              {t("Edit")}
            </Button>
          </div>
        </div>
      ) : null}
    </SideSheet>
  );
}
