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
import tronIcon from "@/assets/tron.svg";
import { requireTurnstile } from "@/components/auth/TurnstileGate";
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
  listWalletTransactions,
  quoteRecharge,
  redeemCard,
  transferReferralRewards,
  type RechargeConfigResponse,
  type RechargeItem,
  type RechargePaymentMethod,
  type RechargeQuoteResponse,
  type TransactionItem,
  type WalletReferralResponse,
  type WalletResponse,
} from "@/lib/wallet-api";
import { createMyInvite, getMyInvite } from "@/lib/iam-api";
import { IamApiError } from "@/lib/api-client";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { formatPoints, formatPointsValue, normalizePointValue } from "@/lib/points";

const { Text } = Typography;
const EPAY_RETURN_MESSAGE = "remail:epay-return";
const EPUSDT_PAYMENT_METHOD = "epusdt_usdt_tron";
const BILLING_PAGE_SIZE = 10;

interface BannerStat {
  icon: ReactNode;
  label: string;
  value: string;
}

function formatDateTime(value: string | undefined) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function configuredPaymentMethods(config: RechargeConfigResponse | null): RechargePaymentMethod[] {
  if (Array.isArray(config?.paymentMethods) && config.paymentMethods.length > 0) {
    return config.paymentMethods as RechargePaymentMethod[];
  }
  return config?.enabled ? ["alipay"] : [];
}

function paymentMethodLabel(method: string, translate: (key: string) => string): string {
  if (method === "alipay") return translate("Alipay");
  if (method === EPUSDT_PAYMENT_METHOD) return translate("USDT (TRON)");
  return method;
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
  const [billingPage, setBillingPage] = useState(1);
  const [debouncedBillingKeyword, flushBillingKeyword] = useDebouncedValue(billingKeyword);
  const [debouncedCustomPoints, flushCustomPoints] = useDebouncedValue(customAmount, 250);
  const [wallet, setWallet] = useState<WalletResponse | null>(null);
  const [referrals, setReferrals] = useState<WalletReferralResponse | null>(null);
  const [rechargeConfig, setRechargeConfig] =
    useState<RechargeConfigResponse | null>(null);
  const [rechargeQuote, setRechargeQuote] =
    useState<RechargeQuoteResponse | null>(null);
  const [referralLink, setReferralLink] = useState("");
  const [recharges, setRecharges] = useState<RechargeItem[]>([]);
  const [redemptions, setRedemptions] = useState<TransactionItem[]>([]);
  const [rechargeHasMore, setRechargeHasMore] = useState(false);
  const [redemptionNextAfterId, setRedemptionNextAfterId] = useState<number>();
  const [redemptionHasMore, setRedemptionHasMore] = useState(false);
  const [walletLoading, setWalletLoading] = useState(true);
  const [referralLoading, setReferralLoading] = useState(false);
  const [transferringRewards, setTransferringRewards] = useState(false);
  const [rechargesLoading, setRechargesLoading] = useState(false);
  const [redemptionsLoading, setRedemptionsLoading] = useState(false);
  const [recharging, setRecharging] = useState(false);
  const [payment, setPayment] = useState<{ rechargeNo: string; url: string; expiresAt: string; method: string } | null>(null);
  const [paymentMethod, setPaymentMethod] = useState<RechargePaymentMethod>("alipay");
  const [paymentOpen, setPaymentOpen] = useState(false);
  const [paymentFrameLoaded, setPaymentFrameLoaded] = useState(false);
  const [paymentFrameSlow, setPaymentFrameSlow] = useState(false);
  const [redeeming, setRedeeming] = useState(false);
  const redeemAttemptRef = useRef<{ code: string; key: string } | null>(null);
  const transferAttemptRef = useRef<string | null>(null);
  const rechargeAttemptRef = useRef<{ points: string; key: string; method: string } | null>(null);
  const paymentFrameRef = useRef<HTMLIFrameElement | null>(null);
  const pendingRechargeNosRef = useRef(new Set<string>());
  const rechargeQuoteSeqRef = useRef(0);
  const rechargeRequestSeqRef = useRef(0);
  const redemptionRequestSeqRef = useRef(0);
  const amountFormApiRef = useRef<{
    setValue?: (field: "topUpCount", value: unknown) => void;
  } | null>(null);
  const redeemFormApiRef = useRef<{
    setValue?: (field: "redemptionCode", value: unknown) => void;
  } | null>(null);
  const billingHasMore = rechargeHasMore || redemptionHasMore;
  const billingLoading = rechargesLoading || redemptionsLoading;

  const openBilling = useCallback(() => {
    setBillingKeyword("");
    setBillingPage(1);
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
      const methods = configuredPaymentMethods(config);
      setPaymentMethod((current) => methods.includes(current) ? current : methods[0] ?? "");
      const first = config.tiers[0];
      if (first) {
        const value = Number(first.points);
        setSelectedAmount(value);
        setCustomAmount(first.points);
        setRechargeQuote(null);
        flushCustomPoints(first.points);
        amountFormApiRef.current?.setValue?.("topUpCount", value);
      }
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error));
    }
  }, [flushCustomPoints, t]);

  const refreshRecharges = useCallback(async () => {
    const seq = rechargeRequestSeqRef.current + 1;
    rechargeRequestSeqRef.current = seq;
    setRechargesLoading(true);
    try {
      const filter = { search: debouncedBillingKeyword.trim() || undefined };
      const response = await listRecharges(filter, 0, 100);
      if (rechargeRequestSeqRef.current !== seq) return;
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
      setRechargeHasMore(response.items.length < response.total);
      if (settled) void refreshMembership();
    } catch (error) {
      if (rechargeRequestSeqRef.current !== seq) return;
      Toast.error(getIamErrorMessage(t, error));
    } finally {
      if (rechargeRequestSeqRef.current === seq) setRechargesLoading(false);
    }
  }, [debouncedBillingKeyword, refreshMembership, t]);

  const refreshRedemptions = useCallback(async () => {
    const seq = redemptionRequestSeqRef.current + 1;
    redemptionRequestSeqRef.current = seq;
    setRedemptionsLoading(true);
    try {
      const response = await listWalletTransactions(
        {
          search: debouncedBillingKeyword.trim() || undefined,
          type: "card_redeem",
        },
        undefined,
        100
      );
      if (redemptionRequestSeqRef.current !== seq) return;
      setRedemptions(response.items);
      setRedemptionNextAfterId(response.nextAfterId);
      setRedemptionHasMore(response.hasNext && Boolean(response.nextAfterId));
    } catch (error) {
      if (redemptionRequestSeqRef.current !== seq) return;
      Toast.error(getIamErrorMessage(t, error));
    } finally {
      if (redemptionRequestSeqRef.current === seq) setRedemptionsLoading(false);
    }
  }, [debouncedBillingKeyword, t]);

  const loadMoreRecharges = useCallback(async () => {
    if (rechargesLoading || !rechargeHasMore) return;
    setRechargesLoading(true);
    const seq = rechargeRequestSeqRef.current;
    try {
      const response = await listRecharges(
        { search: debouncedBillingKeyword.trim() || undefined },
        recharges.length,
        100
      );
      if (rechargeRequestSeqRef.current !== seq) return;
      setRecharges((current) => {
        const existing = new Set(current.map((item) => item.id));
        return [
          ...current,
          ...response.items.filter((item) => !existing.has(item.id)),
        ];
      });
      setRechargeHasMore(recharges.length + response.items.length < response.total);
    } catch (error) {
      if (rechargeRequestSeqRef.current !== seq) return;
      Toast.error(getIamErrorMessage(t, error));
    } finally {
      if (rechargeRequestSeqRef.current === seq) setRechargesLoading(false);
    }
  }, [
    debouncedBillingKeyword,
    rechargeHasMore,
    recharges.length,
    rechargesLoading,
    t,
  ]);

  const loadMoreRedemptions = useCallback(async () => {
    if (
      redemptionsLoading ||
      !redemptionHasMore ||
      !redemptionNextAfterId
    ) return;
    setRedemptionsLoading(true);
    const seq = redemptionRequestSeqRef.current;
    try {
      const response = await listWalletTransactions(
        {
          search: debouncedBillingKeyword.trim() || undefined,
          type: "card_redeem",
        },
        redemptionNextAfterId,
        100
      );
      if (redemptionRequestSeqRef.current !== seq) return;
      setRedemptions((current) => {
        const existing = new Set(current.map((item) => item.id));
        return [
          ...current,
          ...response.items.filter((item) => !existing.has(item.id)),
        ];
      });
      setRedemptionNextAfterId(response.nextAfterId);
      setRedemptionHasMore(response.hasNext && Boolean(response.nextAfterId));
    } catch (error) {
      if (redemptionRequestSeqRef.current !== seq) return;
      Toast.error(getIamErrorMessage(t, error));
    } finally {
      if (redemptionRequestSeqRef.current === seq) setRedemptionsLoading(false);
    }
  }, [
    debouncedBillingKeyword,
    redemptionHasMore,
    redemptionNextAfterId,
    redemptionsLoading,
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
    void refreshRedemptions();
  }, [billingOpen, refreshRecharges, refreshRedemptions]);

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

  const handlePresetSelect = (points: string) => {
    const value = Number(points);
    setSelectedAmount(value);
    setCustomAmount(points);
    flushCustomPoints(points);
    setRechargeQuote(null);
    amountFormApiRef.current?.setValue?.("topUpCount", value);
  };

  useEffect(() => {
    const seq = rechargeQuoteSeqRef.current + 1;
    rechargeQuoteSeqRef.current = seq;
    const points = normalizePointValue(debouncedCustomPoints);
    const pointAmount = Number(points);
    if (
      !rechargeConfig?.enabled ||
      !points ||
      points.includes(".") ||
      pointAmount < Number(rechargeConfig.minPoints)
    ) {
      setRechargeQuote(null);
      return;
    }
    void quoteRecharge(points, paymentMethod || undefined)
      .then((quote) => {
        if (rechargeQuoteSeqRef.current === seq) setRechargeQuote(quote);
      })
      .catch(() => {
        if (rechargeQuoteSeqRef.current === seq) setRechargeQuote(null);
      });
  }, [debouncedCustomPoints, paymentMethod, rechargeConfig]);

  const handleRecharge = async (requestedMethod: RechargePaymentMethod = paymentMethod) => {
    if (recharging) return;
    const methods = configuredPaymentMethods(rechargeConfig);
    if (!rechargeConfig?.enabled || !methods.includes(requestedMethod)) {
      Toast.warning(t("Online recharge is unavailable."));
      return;
    }
    const points = normalizePointValue(customAmount);
    if (points.includes(".")) {
      Toast.warning(t("Amount must be an integer."));
      return;
    }
    if (
      !points ||
      Number(points) < Number(rechargeConfig.minPoints)
    ) {
      Toast.warning(t("Recharge amount is below the minimum."));
      return;
    }
    if (!rechargeAttemptRef.current || rechargeAttemptRef.current.points !== points || rechargeAttemptRef.current.method !== requestedMethod) {
      rechargeAttemptRef.current = { points, key: generateIdempotencyKey(), method: requestedMethod };
    }
    setRecharging(true);
    try {
      const result = await createRecharge(points, rechargeAttemptRef.current.key, requestedMethod);
      rechargeAttemptRef.current = null;
      setPaymentOpen(true);
      setPaymentFrameLoaded(false);
      setPaymentFrameSlow(false);
      setPayment({ rechargeNo: result.recharge.rechargeNo, url: result.payUrl, expiresAt: result.expiresAt, method: result.recharge.paymentMethod || requestedMethod });
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
      const turnstileToken = await requireTurnstile("referral_transfer");
      if (!turnstileToken) return;
      await transferReferralRewards(turnstileToken, transferAttemptRef.current);
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
      const turnstileToken = await requireTurnstile("card_redeem");
      if (!turnstileToken) return;
      await redeemCard(code, turnstileToken, redeemAttemptRef.current.key);
      Toast.success(t("Redemption completed."));
      redeemAttemptRef.current = null;
      setRedemptionCode("");
      redeemFormApiRef.current?.setValue?.("redemptionCode", "");
      await refreshMembership();
      await refreshReferrals();
      if (billingOpen) {
        await refreshRedemptions();
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
        value: walletLoading ? "..." : formatPoints(wallet?.consumerBalance),
      },
      {
        icon: <TrendingUp size={14} />,
        label: "Historical Spend",
        value: walletLoading ? "..." : formatPoints(wallet?.historicalSpend),
      },
      {
        icon: <BarChart2 size={14} />,
        label: "Order Count",
        value: walletLoading ? "..." : String(wallet?.orderCount ?? 0),
      },
    ],
    [t, wallet, walletLoading]
  );

  const referralStats = useMemo<BannerStat[]>(
    () => [
      {
        icon: <TrendingUp size={14} />,
        label: "Pending Rewards",
        value: referralLoading ? "..." : formatPoints(referrals?.pendingRewards),
      },
      {
        icon: <BarChart2 size={14} />,
        label: "Total Earned",
        value: referralLoading ? "..." : formatPoints(referrals?.totalEarned),
      },
      {
        icon: <Users size={14} />,
        label: "Invites",
        value: referralLoading ? "..." : String(referrals?.inviteCount ?? 0),
      },
    ],
    [referralLoading, referrals, t]
  );

  const billingData = useMemo(
    () => [
      ...recharges.map((item) => ({
        ...item,
        orderNo: item.rechargeNo,
        creditedPointsText: formatPointsValue(item.creditedPoints),
        createdAtText: formatDateTime(item.createdAt),
      })),
      ...redemptions.map((item) => ({
        ...item,
        orderNo: item.transactionNo,
        paymentMethod: "Redemption Code",
        creditedPointsText: formatPointsValue(item.amount),
        status: "credited",
        createdAtText: formatDateTime(item.createdAt),
      })),
    ].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt)),
    [recharges, redemptions, t]
  );

  const billingPageCount = Math.max(
    1,
    Math.ceil(billingData.length / BILLING_PAGE_SIZE)
  );
  const safeBillingPage = Math.min(billingPage, billingPageCount);
  const billingPageData = billingData.slice(
    (safeBillingPage - 1) * BILLING_PAGE_SIZE,
    safeBillingPage * BILLING_PAGE_SIZE
  );

  useEffect(() => {
    setBillingPage((page) => Math.min(page, billingPageCount));
  }, [billingPageCount]);

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
        render: (paymentMethod: string) =>
          paymentMethod === "Redemption Code" ? t(paymentMethod) : paymentMethodLabel(paymentMethod, t),
        title: t("Payment method"),
      },
      {
        dataIndex: "creditedPointsText",
        key: "creditedPoints",
        title: t("Credited points"),
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

  const paymentTitle = payment?.method === EPUSDT_PAYMENT_METHOD
    ? t("USDT (TRON) Payment")
    : t("Alipay Payment");

  return (
    <>
      <div className="console-content-width flex flex-col py-5">
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
                          {rechargeQuote ? (
                            <>
                              <span className="block">
                                {t("Credited points")}: {formatPointsValue(rechargeQuote.creditedPoints)}
                              </span>
                              {rechargeQuote.paymentAmount && rechargeQuote.paymentCurrency ? (
                                <span className="block">
                                  {t("Payment amount")}: {rechargeQuote.paymentAmount} {rechargeQuote.paymentCurrency}
                                </span>
                              ) : null}
                            </>
                          ) : t("Enter recharge points for a quote")}
                        </Text>
                      }
                      field="topUpCount"
                      label={t("Recharge points")}
                      max={999999999999}
                      min={Math.max(1, Math.ceil(Number(rechargeConfig?.minPoints) || 1))}
                      onChange={(value) => {
                        const points = normalizePointValue(
                          typeof value === "string" || typeof value === "number"
                            ? value
                            : undefined
                        );
                        const parsed = Number(points);
                        setCustomAmount(points);
                        setSelectedAmount(Number.isFinite(parsed) ? parsed : 0);
                        setRechargeQuote(null);
                      }}
                      precision={0}
                      step={1}
                      style={{ width: "100%" }}
                    />
                    <Form.Slot label={t("Payment Method")}>
                      <Space vertical align="start">
                        {configuredPaymentMethods(rechargeConfig).map((method) => (
                          <Button
                            className="min-h-11"
                            disabled={!rechargeConfig?.enabled || recharging}
                            icon={method === "alipay"
                              ? <SiAlipay color="#1677FF" size={18} />
                              : <img alt="" className="size-[18px]" src={tronIcon} />}
                            key={method}
                            loading={recharging && paymentMethod === method}
                            onClick={() => {
                              setPaymentMethod(method);
                              void handleRecharge(method);
                            }}
                            theme={paymentMethod === method ? "solid" : "outline"}
                            type={paymentMethod === method ? "primary" : "tertiary"}
                          >
                            {method === EPUSDT_PAYMENT_METHOD ? "USDT" : paymentMethodLabel(method, t)}
                          </Button>
                        ))}
                      </Space>
                    </Form.Slot>
                  </div>

                  <Form.Slot label={t("Select recharge points")}>
                    <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
                      {(rechargeConfig?.tiers ?? []).map((tier) => {
                        const selected = Number(tier.points) === selectedAmount;
                        return (
                          <div
                            className="rounded-xl focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--semi-color-primary)]"
                            key={tier.points}
                            onClick={() => handlePresetSelect(tier.points)}
                            onKeyDown={(event) => {
                              if (event.key === "Enter" || event.key === " ") {
                                event.preventDefault();
                                handlePresetSelect(tier.points);
                              }
                            }}
                            role="button"
                            tabIndex={0}
                            style={{
                              cursor: "pointer",
                              height: 132,
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
                                  {formatPointsValue(tier.points)}
                                  {Number(tier.bonusPoints) > 0 ? (
                                    <Tag color="orange" size="small">
                                      +{formatPointsValue(tier.bonusPoints)}
                                    </Tag>
                                  ) : null}
                                </div>
                                <div className="text-xs text-muted-foreground">
                                  {t("Credited points")}: {formatPointsValue(tier.creditedPoints)}
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
                        <span className="flex w-7 shrink-0 items-center justify-center text-muted-foreground">
                          <Gift aria-hidden size={15} />
                        </span>
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
                  {rechargeConfig?.redemptionCodePurchaseUrl ? (
                    <p className="mt-1.5 text-sm text-[var(--semi-color-text-2)]">
                      {t("Looking for a redemption code?")}{" "}
                      <a
                        className="inline-flex min-h-11 cursor-pointer items-center text-foreground underline underline-offset-2 hover:text-brand-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--brand-start)]"
                        href={rechargeConfig.redemptionCodePurchaseUrl}
                        rel="noreferrer"
                        target="_blank"
                      >
                        {t("Buy a redemption code")}
                      </a>
                    </p>
                  ) : null}
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
            onClick={() => {
              if (!payment) return;
              const opened = window.open(payment.url, "_blank", "noopener,noreferrer");
              if (!opened) return;
              setPaymentOpen(false);
              setPaymentFrameLoaded(false);
              setPaymentFrameSlow(false);
            }}
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
        title={paymentTitle}
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
              title={paymentTitle}
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
        bodyStyle={{
          maxHeight: isMobile ? "calc(100vh - 152px)" : "min(720px, calc(100vh - 180px))",
          overflowY: "auto",
        }}
        footer={null}
        onCancel={() => setBillingOpen(false)}
        size={isMobile ? "full-width" : "large"}
        title={t("Recharge Billing")}
        visible={billingOpen}
      >
        <div className="mb-3">
          <Input
            onChange={(value) => {
              setBillingKeyword(String(value));
              setBillingPage(1);
            }}
            placeholder={t("Order No.")}
            prefix={<IconSearch />}
            showClear
            value={billingKeyword}
          />
        </div>
        <div>
          <Table
            columns={billingColumns}
            dataSource={billingPageData}
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
            pagination={{
              currentPage: safeBillingPage,
              onPageChange: setBillingPage,
              pageSize: BILLING_PAGE_SIZE,
              total: billingData.length,
            }}
            rowKey="orderNo"
            scroll={{ x: "max-content" }}
            size="small"
          />
          {billingHasMore ? (
            <div className="mt-3 flex justify-center">
              <Button
                loading={billingLoading}
                onClick={() => {
                  void loadMoreRecharges();
                  void loadMoreRedemptions();
                }}
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
