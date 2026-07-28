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
  Spin,
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
import { MembershipOverview } from "@/components/membership";
import { useAuth } from "@/context/auth-provider";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { generateIdempotencyKey } from "@/lib/idempotency";
import {
  createRecharge,
  getRecharge,
  getRechargeConfig,
  getWallet,
  getWalletReferrals,
  listRecharges,
  redeemCard,
  transferReferralRewards,
  type RechargeConfigResponse,
  type RechargeItem,
  type WalletReferralResponse,
  type WalletResponse,
} from "@/lib/wallet-api";
import { createMyInvite, getMyInvite } from "@/lib/iam-api";
import { IamApiError } from "@/lib/api-client";
import { getIamErrorMessage } from "@/lib/iam-errors";

import { calculateRechargePaymentAmount } from "./wallet-payment";

const { Text } = Typography;
const EPAY_RETURN_MESSAGE = "remail:epay-return";

interface BannerStat {
  icon: ReactNode;
  label: string;
  value: string;
}

function formatCurrency(value: string | number | undefined) {
  const numeric = Number(value ?? 0);
  const safeValue = Number.isFinite(numeric) ? numeric : 0;
  return `￥${safeValue.toLocaleString("zh-CN", {
    maximumFractionDigits: 6,
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
  const { currentUser, refreshCurrentUser } = useAuth();
  const isMobile = useIsMobile();
  const [selectedAmount, setSelectedAmount] = useState(0);
  const [customAmount, setCustomAmount] = useState("");
  const [redemptionCode, setRedemptionCode] = useState("");
  const [billingOpen, setBillingOpen] = useState(false);
  const [billingKeyword, setBillingKeyword] = useState("");
  const [debouncedBillingKeyword, flushBillingKeyword] = useDebouncedValue(billingKeyword);
  const [wallet, setWallet] = useState<WalletResponse | null>(null);
  const [referrals, setReferrals] = useState<WalletReferralResponse | null>(null);
  const [rechargeConfig, setRechargeConfig] =
    useState<RechargeConfigResponse | null>(null);
  const [referralLink, setReferralLink] = useState("");
  const [recharges, setRecharges] = useState<RechargeItem[]>([]);
  const [billingHasMore, setBillingHasMore] = useState(false);
  const [walletLoading, setWalletLoading] = useState(true);
  const [referralLoading, setReferralLoading] = useState(false);
  const [transferringRewards, setTransferringRewards] = useState(false);
  const [billingLoading, setBillingLoading] = useState(false);
  const [recharging, setRecharging] = useState(false);
  const [payment, setPayment] = useState<{ rechargeNo: string; url: string; expiresAt: string } | null>(null);
  const [paymentOpen, setPaymentOpen] = useState(false);
  const [paymentFrameLoaded, setPaymentFrameLoaded] = useState(false);
  const [paymentFrameSlow, setPaymentFrameSlow] = useState(false);
  const [redeeming, setRedeeming] = useState(false);
  const redeemAttemptRef = useRef<{ code: string; key: string } | null>(null);
  const transferAttemptRef = useRef<string | null>(null);
  const rechargeAttemptRef = useRef<{ amount: string; key: string } | null>(null);
  const paymentFrameRef = useRef<HTMLIFrameElement | null>(null);
  const pendingRechargeNosRef = useRef(new Set<string>());
  const billingRequestSeqRef = useRef(0);
  const amountFormApiRef = useRef<{
    setValue?: (field: "topUpCount", value: unknown) => void;
  } | null>(null);
  const redeemFormApiRef = useRef<{
    setValue?: (field: "redemptionCode", value: unknown) => void;
  } | null>(null);

  const openBilling = useCallback(() => {
    setBillingKeyword("");
    flushBillingKeyword("");
    setBillingOpen(true);
  }, [flushBillingKeyword]);

  const refreshWallet = useCallback(async () => {
    setWalletLoading(true);
    try {
      setWallet(await getWallet());
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Wallet load failed."));
    } finally {
      setWalletLoading(false);
    }
  }, [t]);

  const refreshMembership = useCallback(async () => {
    await Promise.all([refreshWallet(), refreshCurrentUser()]);
  }, [refreshCurrentUser, refreshWallet]);

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
      Toast.error(getIamErrorMessage(t, error));
    } finally {
      setReferralLoading(false);
    }
  }, [t]);

  const refreshRechargeConfig = useCallback(async () => {
    try {
      const config = await getRechargeConfig();
      setRechargeConfig(config);
      const first = config.tiers[0];
      if (first) {
        const value = Number(first.amount);
        setSelectedAmount(value);
        setCustomAmount(first.amount);
        amountFormApiRef.current?.setValue?.("topUpCount", value);
      }
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error));
    }
  }, [t]);

  const refreshRecharges = useCallback(async () => {
    const seq = billingRequestSeqRef.current + 1;
    billingRequestSeqRef.current = seq;
    setBillingLoading(true);
    try {
      const response = await listRecharges(
        { search: debouncedBillingKeyword.trim() || undefined },
        0,
        100
      );
      if (billingRequestSeqRef.current !== seq) return;
      const nextPending = new Set(
        response.items
          .filter((item) => ["paying", "callback", "reconciled"].includes(item.status))
          .map((item) => item.rechargeNo)
      );
      const settled = [...pendingRechargeNosRef.current].some(
        (rechargeNo) => !nextPending.has(rechargeNo)
      );
      pendingRechargeNosRef.current = nextPending;
      setRecharges(response.items);
      setBillingHasMore(response.items.length < response.total);
      if (settled) void refreshMembership();
    } catch (error) {
      if (billingRequestSeqRef.current !== seq) return;
      Toast.error(getIamErrorMessage(t, error));
    } finally {
      if (billingRequestSeqRef.current === seq) setBillingLoading(false);
    }
  }, [debouncedBillingKeyword, refreshMembership, t]);

  const loadMoreTransactions = useCallback(async () => {
    if (billingLoading || !billingHasMore) return;
    setBillingLoading(true);
    const seq = billingRequestSeqRef.current;
    try {
      const response = await listRecharges(
        { search: debouncedBillingKeyword.trim() || undefined },
        recharges.length,
        100
      );
      if (billingRequestSeqRef.current !== seq) return;
      setRecharges((current) => {
        const existing = new Set(current.map((item) => item.id));
        return [
          ...current,
          ...response.items.filter((item) => !existing.has(item.id)),
        ];
      });
      setBillingHasMore(recharges.length + response.items.length < response.total);
    } catch (error) {
      if (billingRequestSeqRef.current !== seq) return;
      Toast.error(getIamErrorMessage(t, error));
    } finally {
      if (billingRequestSeqRef.current === seq) setBillingLoading(false);
    }
  }, [
    billingHasMore,
    billingLoading,
    debouncedBillingKeyword,
    recharges.length,
    t,
  ]);

  useEffect(() => {
    void refreshMembership();
  }, [refreshMembership]);

  useEffect(() => {
    void refreshReferrals();
  }, [refreshReferrals]);

  useEffect(() => {
    void refreshRechargeConfig();
  }, [refreshRechargeConfig]);

  useEffect(() => {
    if (!billingOpen) return;
    void refreshRecharges();
  }, [billingOpen, refreshRecharges]);

  useEffect(() => {
    if (!billingOpen || !recharges.some((item) => ["paying", "callback", "reconciled"].includes(item.status))) return;
    const timer = window.setInterval(() => void refreshRecharges(), 2500);
    return () => window.clearInterval(timer);
  }, [billingOpen, recharges, refreshRecharges]);

  useEffect(() => {
    if (!payment || !paymentOpen || paymentFrameLoaded) return;
    setPaymentFrameSlow(false);
    const timer = window.setTimeout(() => setPaymentFrameSlow(true), 10_000);
    return () => window.clearTimeout(timer);
  }, [payment, paymentFrameLoaded, paymentOpen]);

  useEffect(() => {
    if (!payment || !paymentOpen) return;
    const handlePaymentReturn = (event: MessageEvent) => {
      if (
        event.data !== EPAY_RETURN_MESSAGE ||
        event.origin !== window.location.origin ||
        event.source !== paymentFrameRef.current?.contentWindow
      ) return;
      setPaymentOpen(false);
      setPaymentFrameLoaded(false);
      setPaymentFrameSlow(false);
      openBilling();
    };
    window.addEventListener("message", handlePaymentReturn);
    return () => window.removeEventListener("message", handlePaymentReturn);
  }, [openBilling, payment, paymentOpen]);

  useEffect(() => {
    if (!payment) return;
    let cancelled = false;
    let checking = false;
    let consecutiveFailures = 0;
    const refreshPayment = async () => {
      if (cancelled || checking) return;
      const expiresAt = new Date(payment.expiresAt).getTime();
      const expired = Number.isFinite(expiresAt) && Date.now() >= expiresAt;
      if (paymentOpen && !expired) return;
      checking = true;
      try {
        const recharge = await getRecharge(payment.rechargeNo);
        consecutiveFailures = 0;
        if (cancelled) return;
        if (["paying", "callback", "reconciled"].includes(recharge.status)) {
          if (!expired) return;
          setPayment(null);
          setPaymentOpen(false);
          setPaymentFrameLoaded(false);
          setPaymentFrameSlow(false);
          openBilling();
          Toast.error(t("Recharge verification timed out. Please check the billing record."));
          return;
        }
        if (paymentOpen && recharge.status === "credited") return;
        setPayment(null);
        setPaymentOpen(false);
        setPaymentFrameLoaded(false);
        setPaymentFrameSlow(false);
        openBilling();
        if (recharge.status === "credited") {
          Toast.success(t("Recharge successful. Balance has been credited."));
          void refreshMembership();
        } else {
          Toast.error(t("Recharge is abnormal. Please check the billing record."));
        }
      } catch {
        if (cancelled) return;
        consecutiveFailures += 1;
        if (consecutiveFailures === 3) {
          Toast.warning(t("Unable to refresh recharge status. Verification is still running."));
        }
      } finally {
        checking = false;
      }
    };
    void refreshPayment();
    const timer = window.setInterval(() => void refreshPayment(), 2500);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [openBilling, payment, paymentOpen, refreshMembership, t]);

  const handlePresetSelect = (amount: string) => {
    const value = Number(amount);
    setSelectedAmount(value);
    setCustomAmount(amount);
    amountFormApiRef.current?.setValue?.("topUpCount", value);
  };

  const selectedTier = rechargeConfig?.tiers.find(
    (tier) => Number(tier.amount) === selectedAmount
  );
  const payableAmount = useMemo(() => {
    if (selectedTier) return selectedTier.paymentAmount;
    return calculateRechargePaymentAmount(customAmount, rechargeConfig?.feeRate, rechargeConfig?.feeCap);
  }, [customAmount, rechargeConfig?.feeCap, rechargeConfig?.feeRate, selectedTier]);

  const handleRecharge = async () => {
    if (recharging) return;
    if (!rechargeConfig?.enabled) {
      Toast.warning(t("Online recharge is unavailable."));
      return;
    }
    const amount = Number(customAmount);
    if (!Number.isFinite(amount) || amount < Number(rechargeConfig.minAmount)) {
      Toast.warning(t("Recharge amount is below the minimum."));
      return;
    }
    const normalizedAmount = amount.toFixed(2);
    if (!rechargeAttemptRef.current || rechargeAttemptRef.current.amount !== normalizedAmount) {
      rechargeAttemptRef.current = { amount: normalizedAmount, key: generateIdempotencyKey() };
    }
    setRecharging(true);
    try {
      const result = await createRecharge(normalizedAmount, rechargeAttemptRef.current.key);
      rechargeAttemptRef.current = null;
      setPaymentOpen(true);
      setPaymentFrameLoaded(false);
      setPaymentFrameSlow(false);
      setPayment({ rechargeNo: result.recharge.rechargeNo, url: result.payUrl, expiresAt: result.expiresAt });
      void refreshRecharges();
    } catch (error) {
      if (error instanceof IamApiError && error.status >= 400 && error.status < 500) {
        rechargeAttemptRef.current = null;
      }
      Toast.error(getIamErrorMessage(t, error));
    } finally {
      setRecharging(false);
    }
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
    transferAttemptRef.current ??= generateIdempotencyKey();
    try {
      await transferReferralRewards(transferAttemptRef.current);
      Toast.success(t("Transfer completed."));
      transferAttemptRef.current = null;
      await refreshWallet();
      await refreshReferrals();
      if (billingOpen) {
        await refreshRecharges();
      }
    } catch (error) {
      if (error instanceof IamApiError && error.status >= 400 && error.status < 500) {
        transferAttemptRef.current = null;
      }
      Toast.error(getIamErrorMessage(t, error));
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
        key: generateIdempotencyKey(),
      };
    }
    try {
      await redeemCard(code, redeemAttemptRef.current.key);
      Toast.success(t("Redemption completed."));
      redeemAttemptRef.current = null;
      setRedemptionCode("");
      redeemFormApiRef.current?.setValue?.("redemptionCode", "");
      await refreshMembership();
      await refreshReferrals();
      if (billingOpen) {
        await refreshRecharges();
      }
    } catch (error) {
      if (error instanceof IamApiError && error.status >= 400 && error.status < 500) {
        redeemAttemptRef.current = null;
      }
      Toast.error(getIamErrorMessage(t, error));
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
      recharges.map((item) => ({
        ...item,
        orderNo: item.rechargeNo,
        rechargeQuotaText: formatCurrency(item.rechargeQuota),
        paymentAmountText: formatCurrency(item.paymentAmount),
        createdAtText: formatDateTime(item.createdAt),
      })),
    [recharges]
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
        render: (status: string) => (
          <Tag color={status === "credited" ? "green" : status === "failed" ? "red" : "orange"}>
            {t(status)}
          </Tag>
        ),
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
      <div className="console-content-width py-5">
        <MembershipOverview
          currentGroup={currentUser?.userGroup}
          loading={walletLoading}
          onRetry={refreshMembership}
          totalRecharged={wallet?.totalRecharged}
        />

        <div className="mt-5 grid gap-5 xl:grid-cols-2">
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
                            {payableAmount}
                          </span>
                        </Text>
                      }
                      field="topUpCount"
                      label={t("Recharge Amount")}
                      max={999999999}
                      min={Number(rechargeConfig?.minAmount ?? 0.01)}
                      onChange={(value) => {
                        const parsed = Number(value);
                        setCustomAmount(Number.isFinite(parsed) ? String(parsed) : "");
                        setSelectedAmount(Number.isFinite(parsed) ? parsed : 0);
                      }}
                      precision={2}
                      step={0.01}
                      style={{ width: "100%" }}
                    />
                    <Form.Slot label={t("Payment Method")}>
                      <Space vertical align="start">
                        <Button
                          disabled={!rechargeConfig?.enabled}
                          icon={<SiAlipay color="#1677FF" size={18} />}
                          loading={recharging}
                          onClick={() => void handleRecharge()}
                          theme="outline"
                          type="tertiary"
                        >
                          {t("Alipay")}
                        </Button>
                      </Space>
                    </Form.Slot>
                  </div>

                  <Form.Slot label={t("Select recharge amount")}>
                    <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
                      {(rechargeConfig?.tiers ?? []).map((tier) => {
                        const selected = Number(tier.amount) === selectedAmount;
                        return (
                          <div
                            className="rounded-xl focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--semi-color-primary)]"
                            key={tier.amount}
                            onClick={() => handlePresetSelect(tier.amount)}
                            onKeyDown={(event) => {
                              if (event.key === "Enter" || event.key === " ") {
                                event.preventDefault();
                                handlePresetSelect(tier.amount);
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
                                  {formatCurrency(tier.amount)}
                                  {Number(tier.bonus) > 0 ? (
                                    <Tag color="orange" size="small">
                                      +{formatCurrency(tier.bonus)}
                                    </Tag>
                                  ) : null}
                                </div>
                                <div className="text-xs text-muted-foreground">
                                  {t("Pay")} {formatCurrency(tier.paymentAmount)}
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
        bodyStyle={{
          height: isMobile ? "calc(100vh - 152px)" : "min(882px, calc(100vh - 160px))",
          overflow: "hidden",
          padding: 0,
        }}
        footer={
          <Button
            disabled={!payment}
            onClick={() => payment && window.open(payment.url, "_blank", "noopener,noreferrer")}
            theme="outline"
          >
            {t("Open in new window")}
          </Button>
        }
        maskClosable={false}
        onCancel={() => {
          setPaymentOpen(false);
          setPaymentFrameLoaded(false);
          setPaymentFrameSlow(false);
          openBilling();
        }}
        size={isMobile ? "full-width" : "large"}
        title={t("Alipay Payment")}
        visible={Boolean(payment) && paymentOpen}
        width={isMobile ? undefined : "min(960px, calc(100vw - 48px))"}
      >
        {payment ? (
          <div className="relative h-full">
            <iframe
              className={`h-full w-full border-0 bg-white ${paymentFrameLoaded ? "visible" : "invisible"}`}
              onLoad={() => {
                setPaymentFrameLoaded(true);
                setPaymentFrameSlow(false);
              }}
              referrerPolicy="no-referrer"
              ref={paymentFrameRef}
              sandbox="allow-forms allow-same-origin allow-scripts"
              src={payment.url}
              title={t("Alipay Payment")}
            />
            {!paymentFrameLoaded ? (
              <div aria-live="polite" className="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-white px-6 text-center" role="status">
                <Spin size="large" />
                <Text type="secondary">
                  {t(paymentFrameSlow ? "Payment page is taking longer than expected. Open it in a new window." : "Loading payment page...")}
                </Text>
              </div>
            ) : null}
          </div>
        ) : null}
      </Modal>

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
          {billingHasMore ? (
            <div className="mt-3 flex justify-center">
              <Button
                loading={billingLoading}
                onClick={() => void loadMoreTransactions()}
                theme="outline"
              >
                {t("Load more")}
              </Button>
            </div>
          ) : null}
        </div>
      </Modal>
    </>
  );
}
