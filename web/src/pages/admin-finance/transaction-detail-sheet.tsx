import { Button, SideSheet, Tag } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { useIsMobile } from "@/hooks/use-is-mobile";
import type { FinanceTransaction } from "./admin-finance-api";
import {
  formatDateTime,
  formatMoney,
  InfoItem,
  moneyClassName,
  renderDirectionTag,
  renderTransactionTypeTag,
} from "./finance-meta";
import { TransactionAccountCell } from "./transaction-meta";

function reversalTag(record: FinanceTransaction, t: (key: string) => string) {
  if (record.reversalOfNo) {
    return (
      <Tag color="violet" shape="circle" size="small">
        {t("Reversal entry")}
      </Tag>
    );
  }
  if (record.reversed) {
    return (
      <Tag color="grey" shape="circle" size="small">
        {t("Reversed")}
      </Tag>
    );
  }
  return (
    <Tag color="green" shape="circle" size="small">
      {t("Normal")}
    </Tag>
  );
}

export function TransactionDetailSheet({
  transaction,
  onClose,
  onReverse,
  reverseLoading,
}: {
  transaction: FinanceTransaction | null;
  onClose: () => void;
  onReverse?: (transaction: FinanceTransaction) => void;
  reverseLoading?: boolean;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();

  const canReverse = Boolean(
    transaction && !transaction.reversed && !transaction.reversalOfNo
  );

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onClose}
      placement="right"
      title={t("Transaction details")}
      visible={Boolean(transaction)}
      width={isMobile ? "100%" : 940}
    >
      {transaction ? (
        <div className="flex min-h-full flex-col">
          <div className="flex-1 space-y-5 p-5">
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              <InfoItem
                label={t("Transaction No")}
                value={
                  <CopyableTableText
                    copiedText={t("Copied")}
                    text={transaction.transactionNo}
                  />
                }
              />
              <InfoItem
                label={t("Type")}
                value={renderTransactionTypeTag(
                  transaction.transactionType,
                  transaction.direction,
                  t
                )}
              />
              <InfoItem
                label={t("Direction")}
                value={renderDirectionTag(transaction.direction, t)}
              />
              <InfoItem label={t("State")} value={reversalTag(transaction, t)} />
              <InfoItem
                label={t("Amount")}
                value={
                  <span className={moneyClassName(transaction.direction)}>
                    {transaction.direction === "in" ? "+" : "-"}¥
                    {formatMoney(transaction.amount)}
                  </span>
                }
              />
              <InfoItem
                label={t("Balance after")}
                value={
                  <span className="font-mono-data">
                    ¥{formatMoney(transaction.balanceAfter)}
                  </span>
                }
              />
              <InfoItem
                label={t("Balance before")}
                value={
                  <span className="font-mono-data">
                    ¥{formatMoney(transaction.balanceBefore)}
                  </span>
                }
              />
              <InfoItem label={t("Biz type")} value={transaction.bizType || "-"} />
              <InfoItem label={t("Biz ID")} value={transaction.bizId || "-"} />
              {transaction.reversedByNo ? (
                <InfoItem
                  label={t("Reversed by")}
                  value={
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={transaction.reversedByNo}
                    />
                  }
                />
              ) : null}
              {transaction.reversalOfNo ? (
                <InfoItem
                  label={t("Reversal of")}
                  value={
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={transaction.reversalOfNo}
                    />
                  }
                />
              ) : null}
              <InfoItem
                label={t("Created at")}
                value={formatDateTime(transaction.createdAt)}
              />
              <div className="sm:col-span-2 lg:col-span-3">
                <InfoItem
                  label={t("User")}
                  value={
                    <TransactionAccountCell
                      email={transaction.userEmail}
                      groupName={transaction.userGroupName}
                      nickname={transaction.userNickname}
                      role={transaction.userRole}
                      t={t}
                      userId={transaction.userId}
                    />
                  }
                />
              </div>
            </div>
          </div>

          <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
            <Button
              disabled={!canReverse}
              loading={reverseLoading}
              onClick={() => onReverse?.(transaction)}
              type="danger"
            >
              {t("Reverse")}
            </Button>
          </div>
        </div>
      ) : null}
    </SideSheet>
  );
}
