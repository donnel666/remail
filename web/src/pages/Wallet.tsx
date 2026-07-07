import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import {
  Avatar,
  Button,
  Card,
  Empty,
  Form,
  Input,
  Modal,
  Space,
  Tag,
  Table,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import {
  BarChart2,
  Coins,
  Copy,
  CreditCard,
  Gift,
  Receipt,
  Share2,
  TrendingUp,
  Users,
  Wallet as WalletIcon,
  Zap,
} from "lucide-react";
import { SiAlipay } from "react-icons/si";
import { useTranslation } from "react-i18next";

import sampleProjectCover from "@/assets/cover-4.webp";
import { useIsMobile } from "@/hooks/use-is-mobile";
import {
  getWallet,
  getWalletReferrals,
  listWalletTransactions,
  redeemCard,
  transferReferralRewards,
  type TransactionItem,
  type WalletReferralResponse,
  type WalletResponse,
} from "@/lib/wallet-api";
import { createMyInvite, getMyInvite } from "@/lib/iam-api";
import { IamApiError } from "@/lib/api-client";

const { Text } = Typography;

interface BannerStat {
  icon: ReactNode;
  label: string;
  value: string;
}

interface PresetAmount {
  amount: string;
  badge?: string;
  pay: string;
  value: number;
}

interface PaymentMethod {
  name: string;
  type: "alipay";
}

const presetAmounts: PresetAmount[] = [
  { amount: "100 ￥", pay: "￥13.70", value: 100 },
  { amount: "200 ￥", pay: "￥27.40", value: 200 },
  { amount: "300 ￥", pay: "￥41.10", value: 300 },
  { amount: "500 ￥", pay: "￥68.49", value: 500 },
];

const paymentMethods: PaymentMethod[] = [
  { name: "Alipay", type: "alipay" },
];

function formatCurrency(value: string | number | undefined) {
  const numeric = Number(value ?? 0);
  const safeValue = Number.isFinite(numeric) ? numeric : 0;
  return `￥${safeValue.toLocaleString("zh-CN", {
    maximumFractionDigits: 2,
    minimumFractionDigits: 2,
  })}`;
}

function formatDateTime(value: string | undefined) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function buildReferralLink(inviteCode: string | undefined) {
  const code = inviteCode?.trim();
  if (!code || typeof window === "undefined") return "";
  return `${window.location.origin}/register?aff=${encodeURIComponent(code)}`;
}

async function copyText(value: string) {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "true");
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand("copy");
  document.body.removeChild(textarea);
}

async function getOrCreateReferralInvite() {
  try {
    return await getMyInvite();
  } catch (error) {
    if (error instanceof IamApiError && error.status === 404) {
      return createMyInvite();
    }
    throw error;
  }
}

function StatBanner({
  action,
  stats,
  title,
  tone,
}: {
  action?: ReactNode;
  stats: BannerStat[];
  title: string;
  tone: "orange" | "teal";
}) {
  const { t } = useTranslation();
  const overlay =
    tone === "orange"
      ? "linear-gradient(105deg, rgba(160,72,18,0.96), rgba(234,121,37,0.82))"
      : "linear-gradient(105deg, rgba(15,95,91,0.96), rgba(11,130,111,0.82))";

  return (
    <div
      className="h-[130px] bg-cover bg-center text-white"
      style={{
        backgroundImage: `${overlay}, url(${sampleProjectCover})`,
      }}
    >
      <div className="flex h-full flex-col justify-between p-4">
        <div className="flex items-center justify-between gap-3">
          <div className="text-base font-semibold">{t(title)}</div>
          {action}
        </div>
        <div className="grid grid-cols-3 gap-6">
          {stats.map((stat) => (
            <div className="min-w-0 text-center" key={stat.label}>
              <div className="font-mono text-xl font-bold tabular-nums md:text-2xl">
                {stat.value}
              </div>
              <div className="mt-2 flex min-w-0 items-center justify-center gap-1 text-xs text-white/85">
                {stat.icon}
                <span className="truncate">{t(stat.label)}</span>
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

export default function Wallet() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [selectedAmount, setSelectedAmount] = useState(100);
  const [customAmount, setCustomAmount] = useState("100");
  const [redemptionCode, setRedemptionCode] = useState("");
  const [billingOpen, setBillingOpen] = useState(false);
  const [billingKeyword, setBillingKeyword] = useState("");
  const [wallet, setWallet] = useState<WalletResponse | null>(null);
  const [referrals, setReferrals] = useState<WalletReferralResponse | null>(null);
  const [referralLink, setReferralLink] = useState("");
  const [transactions, setTransactions] = useState<TransactionItem[]>([]);
  const [walletLoading, setWalletLoading] = useState(false);
  const [referralLoading, setReferralLoading] = useState(false);
  const [transferringRewards, setTransferringRewards] = useState(false);
  const [billingLoading, setBillingLoading] = useState(false);
  const [redeeming, setRedeeming] = useState(false);
  const redeemAttemptRef = useRef<{ code: string; key: string } | null>(null);
  const amountFormApiRef = useRef<{
    setValue?: (field: "topUpCount", value: unknown) => void;
  } | null>(null);
  const redeemFormApiRef = useRef<{
    setValue?: (field: "redemptionCode", value: unknown) => void;
  } | null>(null);

  const refreshWallet = useCallback(async () => {
    setWalletLoading(true);
    try {
      setWallet(await getWallet());
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : t("Request failed."));
    } finally {
      setWalletLoading(false);
    }
  }, [t]);

  const refreshReferrals = useCallback(async () => {
    setReferralLoading(true);
    try {
      const [stats, invite] = await Promise.all([
        getWalletReferrals(),
        getOrCreateReferralInvite(),
      ]);
      setReferrals(stats);
      setReferralLink(buildReferralLink(invite.inviteCode));
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : t("Request failed."));
    } finally {
      setReferralLoading(false);
    }
  }, [t]);

  const refreshRecharges = useCallback(async () => {
    setBillingLoading(true);
    try {
      const response = await listWalletTransactions(
        { search: billingKeyword.trim() || undefined },
        0,
        100
      );
      setTransactions(response.items);
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : t("Request failed."));
    } finally {
      setBillingLoading(false);
    }
  }, [billingKeyword, t]);

  useEffect(() => {
    void refreshWallet();
  }, [refreshWallet]);

  useEffect(() => {
    void refreshReferrals();
  }, [refreshReferrals]);

  useEffect(() => {
    if (!billingOpen) return;
    void refreshRecharges();
  }, [billingOpen, refreshRecharges]);

  const handlePresetSelect = (preset: PresetAmount) => {
    setSelectedAmount(preset.value);
    setCustomAmount(String(preset.value));
    amountFormApiRef.current?.setValue?.("topUpCount", preset.value);
  };

  const handleMockOnly = (messageKey = "This feature is not connected yet.") => {
    Toast.info(t(messageKey));
  };

  const handleCopyReferral = async () => {
    if (!referralLink) return;
    try {
      await copyText(referralLink);
      Toast.success(t("Copied"));
    } catch {
      Toast.error(t("Copy failed."));
    }
  };

  const handleTransferRewards = async () => {
    if (transferringRewards) return;
    const pending = Number(referrals?.pendingRewards ?? 0);
    if (!Number.isFinite(pending) || pending <= 0) {
      Toast.info(t("No referral rewards available."));
      return;
    }
    setTransferringRewards(true);
    try {
      await transferReferralRewards();
      Toast.success(t("Transfer completed."));
      await refreshWallet();
      await refreshReferrals();
      if (billingOpen) {
        await refreshRecharges();
      }
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : t("Request failed."));
    } finally {
      setTransferringRewards(false);
    }
  };

  const handleRedeem = async () => {
    if (!redemptionCode.trim()) {
      Toast.warning(t("Please enter redemption code."));
      return;
    }
    if (redeeming) return;
    setRedeeming(true);
    const code = redemptionCode.trim();
    if (!redeemAttemptRef.current || redeemAttemptRef.current.code !== code) {
      redeemAttemptRef.current = {
        code,
        key:
          typeof crypto !== "undefined" && "randomUUID" in crypto
            ? crypto.randomUUID()
            : `${Date.now()}-${Math.random().toString(36).slice(2)}`,
      };
    }
    try {
      await redeemCard(code, redeemAttemptRef.current.key);
      Toast.success(t("Redemption completed."));
      redeemAttemptRef.current = null;
      setRedemptionCode("");
      redeemFormApiRef.current?.setValue?.("redemptionCode", "");
      await refreshWallet();
      await refreshReferrals();
      if (billingOpen) {
        await refreshRecharges();
      }
    } catch (error) {
      if (error instanceof IamApiError && error.status >= 400 && error.status < 500) {
        redeemAttemptRef.current = null;
      }
      Toast.error(error instanceof Error ? error.message : t("Request failed."));
    } finally {
      setRedeeming(false);
    }
  };

  const rechargeStats = useMemo<BannerStat[]>(
    () => [
      {
        icon: <WalletIcon size={14} />,
        label: "Current Balance",
        value: walletLoading ? "..." : formatCurrency(wallet?.consumerBalance),
      },
      {
        icon: <TrendingUp size={14} />,
        label: "Historical Spend",
        value: walletLoading ? "..." : formatCurrency(wallet?.historicalSpend),
      },
      {
        icon: <BarChart2 size={14} />,
        label: "Order Count",
        value: walletLoading ? "..." : String(wallet?.orderCount ?? 0),
      },
    ],
    [wallet, walletLoading]
  );

  const referralStats = useMemo<BannerStat[]>(
    () => [
      {
        icon: <TrendingUp size={14} />,
        label: "Pending Rewards",
        value: referralLoading ? "..." : formatCurrency(referrals?.pendingRewards),
      },
      {
        icon: <BarChart2 size={14} />,
        label: "Total Earned",
        value: referralLoading ? "..." : formatCurrency(referrals?.totalEarned),
      },
      {
        icon: <Users size={14} />,
        label: "Invites",
        value: referralLoading ? "..." : String(referrals?.inviteCount ?? 0),
      },
    ],
    [referralLoading, referrals]
  );

  const billingData = useMemo(
    () =>
      transactions.map((item) => ({
        ...item,
        orderNo: item.transactionNo,
        paymentMethod: item.transactionType === "card_redeem" ? "Redemption Code" : item.bizType,
        rechargeQuotaText: formatCurrency(item.amount),
        paymentAmountText: formatCurrency(item.amount),
        status: item.direction === "in" ? "credited" : item.transactionType,
        createdAtText: formatDateTime(item.createdAt),
      })),
    [transactions]
  );

  const billingColumns = useMemo(
    () => [
      {
        dataIndex: "orderNo",
        key: "orderNo",
        title: t("Order No."),
      },
      {
        dataIndex: "paymentMethod",
        key: "paymentMethod",
        title: t("Payment method"),
      },
      {
        dataIndex: "rechargeQuotaText",
        key: "rechargeQuota",
        title: t("Recharge quota"),
      },
      {
        dataIndex: "paymentAmountText",
        key: "paymentAmount",
        title: t("Payment amount"),
      },
      {
        dataIndex: "status",
        key: "status",
        render: (status: string) => <Tag>{t(status)}</Tag>,
        title: t("Status"),
      },
      {
        dataIndex: "createdAtText",
        key: "createdAt",
        title: t("Created At"),
      },
    ],
    [t]
  );

  return (
    <>
      <div className="mx-auto max-w-[1280px] px-2 pt-5">
        <div className="grid gap-5 xl:grid-cols-2">
          <div className="space-y-2">
            <Card
              bodyStyle={{ padding: 12 }}
              className="!rounded-2xl border-0 shadow-sm"
            >
              <div className="mb-3 flex items-center justify-between gap-3">
                <div className="flex min-w-0 items-center">
                  <Avatar
                    className="mr-3 shadow-md"
                    color="orange"
                    size="small"
                  >
                    <CreditCard size={16} />
                  </Avatar>
                  <div>
                    <Text className="text-lg font-medium">
                      {t("Account Recharge")}
                    </Text>
                    <div className="text-xs">
                      {t("Multiple recharge methods, safe and convenient")}
                    </div>
                  </div>
                </div>
                <Button
                  icon={<Receipt size={16} />}
                  onClick={() => setBillingOpen(true)}
                  theme="solid"
                  type="primary"
                >
                  {t("Billing")}
                </Button>
              </div>

              <Card
                bodyStyle={{ padding: 8 }}
                className="!rounded-xl w-full overflow-hidden"
                cover={
                  <StatBanner
                    stats={rechargeStats}
                    title="Account statistics"
                    tone="orange"
                  />
                }
              >
                <Form
                  getFormApi={(api) => {
                    amountFormApiRef.current = api;
                  }}
                  initValues={{ topUpCount: Number(customAmount) || 0 }}
                >
                  <div className="grid gap-4 md:grid-cols-[213px_minmax(0,1fr)] md:gap-8">
                    <Form.InputNumber
                      extraText={
                        <Text type="secondary">
                          {t("Payable")}:{" "}
                          <span style={{ color: "red" }}>
                            {Number(selectedAmount || 0).toFixed(2)}
                          </span>
                        </Text>
                      }
                      field="topUpCount"
                      label={t("Recharge Amount")}
                      max={999999999}
                      min={1}
                      onChange={(value) => {
                        const parsed = Number(value);
                        setCustomAmount(Number.isFinite(parsed) ? String(parsed) : "");
                        if (Number.isFinite(parsed) && parsed > 0) {
                          setSelectedAmount(parsed);
                        }
                      }}
                      precision={0}
                      step={1}
                      style={{ width: "100%" }}
                    />
                    <Form.Slot label={t("Payment Method")}>
                      <Space wrap>
                        {paymentMethods.map((method) => (
                          <Button
                            icon={<SiAlipay color="#1677FF" size={18} />}
                            key={method.type}
                            onClick={() => handleMockOnly()}
                            theme="outline"
                            type="tertiary"
                          >
                            {t(method.name)}
                          </Button>
                        ))}
                      </Space>
                    </Form.Slot>
                  </div>

                  <Form.Slot label={t("Select recharge amount")}>
                    <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
                      {presetAmounts.map((preset) => {
                        const selected = preset.value === selectedAmount;
                        return (
                          <div
                            key={preset.value}
                            onClick={() => handlePresetSelect(preset)}
                            onKeyDown={(event) => {
                              if (event.key === "Enter" || event.key === " ") {
                                event.preventDefault();
                                handlePresetSelect(preset);
                              }
                            }}
                            role="button"
                            tabIndex={0}
                            style={{
                              cursor: "pointer",
                              height: 116,
                              width: "100%",
                            }}
                          >
                            <Card
                              bodyStyle={{ padding: 12 }}
                              className="!rounded-xl h-full"
                              style={{
                                border: selected
                                  ? "2px solid var(--semi-color-primary)"
                                  : "1px solid var(--semi-color-border)",
                                height: "100%",
                                width: "100%",
                              }}
                            >
                              <div className="text-center">
                                <div className="mb-2 flex items-center justify-center gap-1 text-base font-semibold">
                                  <Coins size={18} />
                                  {preset.amount}
                                  {preset.badge ? (
                                    <Tag color="orange" size="small">
                                      {t(preset.badge)}
                                    </Tag>
                                  ) : null}
                                </div>
                                <div className="text-xs text-muted-foreground">
                                  {t("Pay")} {preset.pay}
                                  {t("Pay saving suffix")}
                                </div>
                              </div>
                            </Card>
                          </div>
                        );
                      })}
                    </div>
                  </Form.Slot>
                </Form>
              </Card>

              <div className="mt-2">
                <Card
                  bodyStyle={{ padding: "6px 12px 12px" }}
                  className="!rounded-xl w-full"
                  title={
                    <Text strong type="tertiary">
                      {t("Redemption Code Recharge")}
                    </Text>
                  }
                >
                  <Form
                    getFormApi={(api) => {
                      redeemFormApiRef.current = api;
                    }}
                    initValues={{ redemptionCode }}
                  >
                    <Form.Input
                      field="redemptionCode"
                      noLabel
                      onChange={(value) => setRedemptionCode(String(value))}
                      placeholder={t("Enter redemption code")}
                      prefix={
                        <Gift
                          className="text-muted-foreground"
                          size={15}
                        />
                      }
                      showClear
                      style={{ width: "100%" }}
                      suffix={
                        <Button
                          loading={redeeming}
                          onClick={handleRedeem}
                          theme="solid"
                          type="primary"
                        >
                          {t("Redeem quota")}
                        </Button>
                      }
                    />
                  </Form>
                </Card>
              </div>
            </Card>
          </div>

          <Card
            bodyStyle={{ padding: 12 }}
            className="!rounded-2xl flex h-full flex-col border-0 shadow-sm"
          >
            <div className="mb-3 flex items-center justify-between gap-3">
              <div className="flex min-w-0 items-center">
                <Avatar className="mr-3 shadow-md" color="green" size="small">
                  <Share2 size={16} />
                </Avatar>
                <div>
                  <Text className="text-lg font-medium">
                    {t("Referral Rewards")}
                  </Text>
                  <div className="text-xs">
                    {t("Invite friends for additional rewards")}
                  </div>
                </div>
              </div>
            </div>

            <Card
              bodyStyle={{ padding: 12 }}
              className="!rounded-xl w-full overflow-hidden"
              cover={
                <StatBanner
                  action={
                    <Button
                      disabled={Number(referrals?.pendingRewards ?? 0) <= 0}
                      icon={<Zap size={12} />}
                      loading={transferringRewards}
                      onClick={handleTransferRewards}
                      size="small"
                      theme="solid"
                      type="primary"
                    >
                      {t("Transfer to Balance")}
                    </Button>
                  }
                  stats={referralStats}
                  title="Reward statistics"
                  tone="teal"
                />
              }
            >
              <Input
                className="!rounded-lg"
                prefix={
                  <span className="whitespace-nowrap pr-3 text-sm text-muted-foreground">
                    {t("Referral Link")}
                  </span>
                }
                placeholder={t("Referral link is loading.")}
                readOnly
                value={referralLink}
                suffix={
                  <Button
                    disabled={!referralLink}
                    icon={<Copy size={14} />}
                    loading={referralLoading}
                    onClick={handleCopyReferral}
                    theme="solid"
                    type="primary"
                  >
                    {t("Copy")}
                  </Button>
                }
              />
            </Card>

            <div className="mt-2">
              <Card
                className="!rounded-xl w-full"
                title={<Text type="tertiary">{t("Reward Rules")}</Text>}
              >
                <div className="space-y-3 p-3 text-sm text-muted-foreground">
                  {[
                    "Invite friends to register and receive rewards after they recharge.",
                    "Transfer rewards into consumer balance after settlement.",
                    "More invited active users bring more rewards.",
                  ].map((item) => (
                    <div className="flex gap-2" key={item}>
                      <span className="mt-2 size-1.5 shrink-0 rounded-full bg-emerald-500" />
                      <span>{t(item)}</span>
                    </div>
                  ))}
                </div>
              </Card>
            </div>
          </Card>
        </div>
      </div>

      <Modal
        footer={null}
        onCancel={() => setBillingOpen(false)}
        size={isMobile ? "full-width" : "large"}
        title={t("Recharge Billing")}
        visible={billingOpen}
      >
        <div className="mb-3">
          <Input
            onChange={(value) => setBillingKeyword(String(value))}
            placeholder={t("Order No.")}
            prefix={<IconSearch />}
            showClear
            value={billingKeyword}
          />
        </div>
        <div>
          <Table
            columns={billingColumns}
            dataSource={billingData}
            empty={
              <Empty
                darkModeImage={
                  <IllustrationNoResultDark
                    style={{ height: 150, width: 150 }}
                  />
                }
                description={t("No recharge records")}
                image={
                  <IllustrationNoResult style={{ height: 150, width: 150 }} />
                }
                style={{ padding: 30 }}
              />
            }
            loading={billingLoading}
            pagination={false}
            rowKey="orderNo"
            size="small"
          />
        </div>
      </Modal>
    </>
  );
}
