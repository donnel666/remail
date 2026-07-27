import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import {
  Avatar,
  Button,
  Card,
  Empty,
  Input,
  Modal,
  Skeleton,
  Space,
  Tag,
  TextArea,
  Toast,
} from "@douyinfe/semi-ui";
import {
  ArrowRightLeft,
  CircleDollarSign,
  RefreshCw,
  Snowflake,
  Upload,
  Wallet,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { CardTable } from "@/components/semi/card-table";
import { useAuth } from "@/context/auth-provider";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { generateIdempotencyKey } from "@/lib/idempotency";
import {
  getWallet,
  listWalletTransactions,
  transferSupplierBalance,
  type TransactionItem,
  type WalletResponse,
} from "@/lib/wallet-api";

import { formatMoney } from "./admin-users/format-money";
import {
  createSupplierApplicationTicket,
  hasSupplierRole,
} from "./resources/supplier-application-modal";
import {
  buildAlipayWithdrawalTicketInput,
  isPositiveLedgerAmount,
  sumLedgerAmounts,
  validateWithdrawal,
  type WithdrawalDestination,
} from "./finance-withdrawal";
import { createTicket } from "./tickets/tickets-api";

const MAX_QR_CODE_BYTES = 5 * 1024 * 1024;

function moneyText(value: string | null | undefined) {
  return value == null ? "--" : `¥${formatMoney(value)}`;
}

function formatDateTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
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

function MetricRow({
  color,
  icon,
  label,
  loading,
  value,
}: {
  color: "blue" | "cyan" | "green" | "orange" | "purple";
  icon: ReactNode;
  label: string;
  loading: boolean;
  value: string;
}) {
  return (
    <div className="flex items-center">
      <Avatar className="mr-3 shrink-0" color={color} size="small">
        {icon}
      </Avatar>
      <div className="min-w-0">
        <div className="truncate text-xs text-[var(--semi-color-text-2)]">{label}</div>
        <div className="font-mono-data text-lg font-semibold text-[var(--semi-color-text-0)]">
          <Skeleton
            active
            loading={loading}
            placeholder={<Skeleton.Paragraph rows={1} style={{ height: 24, marginTop: 4, width: 82 }} />}
          >
            {value}
          </Skeleton>
        </div>
      </div>
    </div>
  );
}

function SupplierApplicationPanel() {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [submitted, setSubmitted] = useState(false);

  const submit = async () => {
    const value = reason.trim();
    if (!value) {
      Toast.warning(t("Please enter supplier application reason."));
      return;
    }
    setSubmitting(true);
    try {
      await createSupplierApplicationTicket(value);
      setReason("");
      setSubmitted(true);
      Toast.success(t("Supplier application submitted."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Supplier application failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="console-content-width console-dashboard-page flex min-h-full items-start justify-center py-5 md:pt-16">
      <Card
        className="w-full max-w-2xl !rounded-2xl"
        headerLine
        title={
          <h1 className="m-0 flex items-center gap-2 text-base font-semibold">
            <CircleDollarSign size={18} />
            {t("Apply supplier")}
          </h1>
        }
      >
        <p className="mb-4 text-sm leading-6 text-[var(--semi-color-text-2)]">
          {t("Supplier access is required to use the finance center.")}
        </p>
        {submitted ? (
          <div className="rounded-xl bg-[var(--semi-color-success-light-default)] p-4">
            <Tag color="green" shape="circle">
              {t("Submitted")}
            </Tag>
            <p className="mb-0 mt-3 text-sm text-[var(--semi-color-text-1)]">
              {t("Supplier application submitted. Access will be enabled after manual review.")}
            </p>
          </div>
        ) : (
          <div className="space-y-4">
            <label className="block" htmlFor="supplier-application-reason">
              <span className="mb-2 block text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Application reason")}
              </span>
              <TextArea
                autosize={{ minRows: 6, maxRows: 10 }}
                id="supplier-application-reason"
                maxCount={1000}
                onChange={(value) => setReason(String(value))}
                placeholder={t("Supplier application reason placeholder")}
                showClear
                value={reason}
              />
            </label>
            <div className="flex justify-end">
              <Button loading={submitting} onClick={() => void submit()} type="primary">
                {submitting ? t("Submitting") : t("Submit application")}
              </Button>
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}

export default function FinanceCenter() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const supplier = hasSupplierRole(currentUser?.role);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const requestSequence = useRef(0);
  const fileReadSequence = useRef(0);
  const transferAttemptRef = useRef<{ amount: string; key: string } | null>(null);
  const [wallet, setWallet] = useState<WalletResponse | null>(null);
  const [transactions, setTransactions] = useState<TransactionItem[]>([]);
  const [walletLoading, setWalletLoading] = useState(supplier);
  const [transactionsLoading, setTransactionsLoading] = useState(supplier);
  const [withdrawOpen, setWithdrawOpen] = useState(false);
  const [withdrawAmount, setWithdrawAmount] = useState("");
  const [destination, setDestination] = useState<WithdrawalDestination>("alipay");
  const [note, setNote] = useState("");
  const [paymentQrCode, setPaymentQrCode] = useState("");
  const [paymentQrCodeName, setPaymentQrCodeName] = useState("");
  const [withdrawing, setWithdrawing] = useState(false);

  const load = useCallback(async () => {
    if (!supplier) return;
    const requestId = ++requestSequence.current;
    setWalletLoading(true);
    setTransactionsLoading(true);

    const walletRequest = getWallet()
      .then((response) => {
        if (requestId === requestSequence.current) setWallet(response);
      })
      .catch((error) => {
        if (requestId === requestSequence.current) {
          Toast.error(getIamErrorMessage(t, error, "Supplier finance load failed."));
        }
      })
      .finally(() => {
        if (requestId === requestSequence.current) setWalletLoading(false);
      });

    const transactionsRequest = listWalletTransactions({}, undefined, 100)
      .then((response) => {
        if (requestId !== requestSequence.current) return;
        setTransactions(
          response.items
            .filter((item) => item.balanceBucket !== "consumer")
            .slice(0, 20),
        );
      })
      .catch((error) => {
        if (requestId === requestSequence.current) {
          Toast.error(getIamErrorMessage(t, error, "Supplier transactions load failed."));
        }
      })
      .finally(() => {
        if (requestId === requestSequence.current) setTransactionsLoading(false);
      });

    await Promise.all([walletRequest, transactionsRequest]);
  }, [supplier, t]);

  useEffect(() => {
    if (!supplier) {
      setWalletLoading(false);
      setTransactionsLoading(false);
      return undefined;
    }
    void load();
    return () => {
      requestSequence.current += 1;
      fileReadSequence.current += 1;
    };
  }, [load, supplier]);

  const closeWithdrawal = () => {
    fileReadSequence.current += 1;
    setWithdrawOpen(false);
    setWithdrawAmount("");
    setDestination("alipay");
    setNote("");
    setPaymentQrCode("");
    setPaymentQrCodeName("");
    setWithdrawing(false);
    transferAttemptRef.current = null;
  };

  const pickQrCode = async (file?: File) => {
    if (!file) return;
    if (!file.type.startsWith("image/")) {
      Toast.warning(t("Invalid payment QR code."));
      return;
    }
    if (file.size > MAX_QR_CODE_BYTES) {
      Toast.warning(t("Payment QR code must not exceed 5 MB."));
      return;
    }
    const readId = ++fileReadSequence.current;
    try {
      const dataUrl = await readImageAsDataUrl(file);
      if (readId !== fileReadSequence.current) return;
      setPaymentQrCode(dataUrl);
      setPaymentQrCodeName(file.name);
    } catch {
      if (readId === fileReadSequence.current) {
        Toast.error(t("Invalid payment QR code."));
      }
    } finally {
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  };

  const submitWithdrawal = async () => {
    if (withdrawing) return;
    const validationError = validateWithdrawal({
      amount: withdrawAmount,
      available: wallet?.supplierAvailable ?? "0",
      destination,
      paymentQrCode,
    });
    if (validationError) {
      Toast.warning(t(validationError));
      return;
    }
    setWithdrawing(true);
    try {
      if (destination === "wallet") {
        const amount = withdrawAmount.trim();
        let attempt = transferAttemptRef.current;
        if (!attempt || attempt.amount !== amount) {
          attempt = { amount, key: generateIdempotencyKey() };
          transferAttemptRef.current = attempt;
        }
        const updatedWallet = await transferSupplierBalance(amount, attempt.key);
        setWallet(updatedWallet);
        Toast.success(t("Wallet transfer completed."));
        closeWithdrawal();
        void load();
        return;
      }
      await createTicket(
        buildAlipayWithdrawalTicketInput({
          amount: withdrawAmount,
          note,
          paymentQrCode,
        }),
      );
      Toast.success(t("Withdrawal submitted."));
      closeWithdrawal();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(
          t,
          error,
          destination === "wallet" ? "Wallet transfer failed." : "Withdrawal submission failed.",
        ),
      );
    } finally {
      setWithdrawing(false);
    }
  };

  const columns = useMemo(
    () => [
      {
        title: t("Type"),
        dataIndex: "transactionType",
        width: 115,
        render: (value: unknown, record: TransactionItem) => (
          <Tag color={record.direction === "in" ? "green" : "orange"} shape="circle">
            {t(String(value))}
          </Tag>
        ),
      },
      {
        title: t("Account"),
        dataIndex: "balanceBucket",
        width: 125,
        render: (value: unknown) =>
          t(value === "supplier_available" ? "Supplier available" : "Frozen amount"),
      },
      {
        title: t("Amount"),
        dataIndex: "amount",
        width: 115,
        render: (value: unknown, record: TransactionItem) => (
          <span
            className={`font-mono-data ${
              record.direction === "in"
                ? "text-[var(--semi-color-success)]"
                : "text-[var(--semi-color-warning)]"
            }`}
          >
            {`${record.direction === "in" ? "+" : "-"}¥${formatMoney(
              String(value).replace(/^-/, ""),
            )}`}
          </span>
        ),
      },
      {
        title: t("Balance after"),
        dataIndex: "balanceAfter",
        width: 135,
        render: (value: unknown) => (
          <span className="font-mono-data">¥{formatMoney(String(value))}</span>
        ),
      },
      {
        title: t("Created at"),
        dataIndex: "createdAt",
        width: 190,
        render: (value: unknown) => formatDateTime(String(value)),
      },
    ],
    [t],
  );

  if (!supplier) return <SupplierApplicationPanel />;

  const supplierAvailable = wallet?.supplierAvailable;
  const supplierFrozen = wallet?.supplierFrozen;
  const supplierIncome = wallet
    ? sumLedgerAmounts(wallet.supplierAvailable, wallet.supplierFrozen)
    : null;
  const canWithdraw = isPositiveLedgerAmount(supplierAvailable ?? "");
  const refreshing = walletLoading || transactionsLoading;

  return (
    <div className="console-content-width console-dashboard-page h-full py-5">
      <div className="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="m-0 text-2xl font-semibold text-[var(--semi-color-text-0)]">
            {t("Finance Center")}
          </h1>
          <p className="mb-0 mt-1 text-sm text-[var(--semi-color-text-2)]">
            {t("Manage supplier income, withdrawals and transfers.")}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            aria-label={t("Refresh")}
            icon={<RefreshCw size={16} />}
            loading={refreshing}
            onClick={() => void load()}
            theme="borderless"
            type="tertiary"
          >
            {t("Refresh")}
          </Button>
          <Button
            disabled={!canWithdraw || walletLoading}
            icon={<CircleDollarSign size={16} />}
            onClick={() => {
              setDestination("alipay");
              setWithdrawOpen(true);
            }}
            type="primary"
          >
            {t("Withdraw to Alipay")}
          </Button>
          <Button
            disabled={!canWithdraw || walletLoading}
            icon={<ArrowRightLeft size={16} />}
            onClick={() => {
              setDestination("wallet");
              setWithdrawOpen(true);
            }}
            theme="outline"
          >
            {t("Transfer to user wallet")}
          </Button>
        </div>
      </div>

      <div className="mb-4 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card
          bodyStyle={{ padding: 10 }}
          bordered
          className="console-dashboard-stat-card w-full border-0 bg-[color-mix(in_oklch,#22a06b_12%,var(--semi-color-bg-0))] !rounded-2xl"
          headerLine
          title={
            <div className="flex items-center gap-2">
              <CircleDollarSign size={16} />
              {t("Supplier income")}
            </div>
          }
        >
          <div className="grid gap-4 sm:grid-cols-3 lg:grid-cols-1">
            <MetricRow
              color="green"
              icon={<CircleDollarSign size={16} />}
              label={t("Income amount")}
              loading={walletLoading}
              value={moneyText(supplierIncome)}
            />
            <MetricRow
              color="blue"
              icon={<ArrowRightLeft size={16} />}
              label={t("Withdrawable balance")}
              loading={walletLoading}
              value={moneyText(supplierAvailable)}
            />
            <MetricRow
              color="purple"
              icon={<Snowflake size={16} />}
              label={t("Frozen amount")}
              loading={walletLoading}
              value={moneyText(supplierFrozen)}
            />
          </div>
        </Card>

        <Card
          bodyStyle={{ padding: 10 }}
          bordered
          className="console-dashboard-stat-card w-full border-0 bg-[color-mix(in_oklch,#3b82f6_12%,var(--semi-color-bg-0))] !rounded-2xl"
          headerLine
          title={
            <div className="flex items-center gap-2">
              <Wallet size={16} />
              {t("Account overview")}
            </div>
          }
        >
          <MetricRow
            color="blue"
            icon={<Wallet size={16} />}
            label={t("User wallet")}
            loading={walletLoading}
            value={moneyText(wallet?.consumerBalance)}
          />
        </Card>
      </div>

      <section className="mb-4">
        <h2 className="mb-3 flex items-center gap-2 text-base font-semibold text-[var(--semi-color-text-0)]">
          <ArrowRightLeft size={16} />
          {t("Recent transactions")}
        </h2>
        <CardTable
          className="overflow-hidden rounded-xl"
          columns={columns}
          dataSource={transactions}
          empty={<Empty description={t("No transactions found")} />}
          hidePagination
          loading={transactionsLoading}
          pagination={false}
          rowKey="id"
          scroll={{ x: "max(100%, 680px)" }}
          size="middle"
        />
      </section>

      <Modal
        footer={
          <Space>
            <Button disabled={withdrawing} onClick={closeWithdrawal} theme="outline">
              {t("Cancel")}
            </Button>
            <Button loading={withdrawing} onClick={() => void submitWithdrawal()} type="primary">
              {withdrawing
                ? t(destination === "wallet" ? "Transferring" : "Submitting")
                : t(destination === "wallet" ? "Confirm transfer" : "Submit")}
            </Button>
          </Space>
        }
        maskClosable={!withdrawing}
        onCancel={() => {
          if (!withdrawing) closeWithdrawal();
        }}
        title={t(destination === "wallet" ? "Transfer supplier balance" : "Supplier withdrawal application")}
        visible={withdrawOpen}
        width={isMobile ? "94%" : 560}
      >
        <div className="space-y-4">
          <label className="block" htmlFor="withdraw-amount">
            <span className="mb-2 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t(destination === "wallet" ? "Transfer amount" : "Withdraw amount")}
            </span>
            <Input
              id="withdraw-amount"
              inputMode="decimal"
              onChange={(value) => setWithdrawAmount(String(value))}
              placeholder="0.00"
              prefix="¥"
              value={withdrawAmount}
            />
            <span className="mt-1 block text-xs text-[var(--semi-color-text-2)]">
              {t("Withdrawable balance")}: {moneyText(supplierAvailable)}
            </span>
          </label>

          {destination === "alipay" ? (
            <div>
              <div className="mb-2 text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Alipay payment QR code")}
              </div>
              <input
                accept="image/*"
                className="hidden"
                onChange={(event) => void pickQrCode(event.target.files?.[0])}
                ref={fileInputRef}
                type="file"
              />
              {paymentQrCode ? (
                <div className="relative overflow-hidden rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
                  <img
                    alt={t("Alipay payment QR code")}
                    className="mx-auto max-h-56 max-w-full object-contain"
                    src={paymentQrCode}
                  />
                  <Button
                    aria-label={t("Remove payment QR code")}
                    className="!absolute right-2 top-2"
                    icon={<X size={15} />}
                    onClick={() => {
                      setPaymentQrCode("");
                      setPaymentQrCodeName("");
                    }}
                    size="small"
                    theme="solid"
                    type="tertiary"
                  />
                  <div className="mt-2 truncate text-center text-xs text-[var(--semi-color-text-2)]">
                    {paymentQrCodeName}
                  </div>
                </div>
              ) : (
                <Button icon={<Upload size={16} />} onClick={() => fileInputRef.current?.click()} theme="outline">
                  {t("Upload payment QR code")}
                </Button>
              )}
              <div className="mt-1 text-xs text-[var(--semi-color-text-2)]">
                {t("Image files only, up to 5 MB.")}
              </div>
            </div>
          ) : null}

          {destination === "alipay" ? (
            <label className="block" htmlFor="withdraw-note">
              <span className="mb-2 block text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Note")}
              </span>
              <TextArea
                autosize={{ minRows: 3, maxRows: 6 }}
                id="withdraw-note"
                maxCount={500}
                onChange={(value) => setNote(String(value))}
                placeholder={t("Withdrawal request note placeholder")}
                showClear
                value={note}
              />
            </label>
          ) : null}
        </div>
      </Modal>
    </div>
  );
}
