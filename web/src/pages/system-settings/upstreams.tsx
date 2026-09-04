import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Modal,
  Select,
  Spin,
  Switch,
  Table,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import {
	CheckCircle2,
  CreditCard,
  History,
  RefreshCw,
  Save,
  ShoppingCart,
  Smartphone,
  Trash2,
	WalletCards,
	XCircle,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";
import {
	getKitesimUpstream,
	purchaseKitesimNumbers,
	rechargeKitesimAccount,
	reconcileKitesimOperation,
	refreshKitesimUpstream,
	resolveKitesimOperation,
  updateKitesimUpstream,
  type KitesimCardProfile,
  type KitesimOperation,
  type KitesimOperationStatus,
  type KitesimProduct,
  type KitesimTaskStatus,
  type KitesimUpstreamView,
} from "@/lib/kitesim-upstream-api";
import type { SectionProps } from "./index";
import {
  SettingsAccessBoundary,
  SettingsCardHeader,
  SettingsFormGrid,
  SettingsNumberField,
  SettingsSection,
  SettingsTextField,
} from "./settings-layout";

const { Text } = Typography;

function formatTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

function formatPoints(value?: string) {
  const number = Number(value ?? 0);
  return Number.isFinite(number)
    ? number.toFixed(6).replace(/\.?0+$/, "")
    : value ?? "0";
}

const EMPTY_KITESIM_CARD: KitesimCardProfile = {
  number: "",
  expiryMonth: 0,
  expiryYear: 0,
  holder: "",
  billingEmail: "",
  firstName: "",
  lastName: "",
  phone: "",
  country: "",
  city: "",
  address: "",
};

const KITESIM_BILLING_DEFAULTS = {
  firstName: "noreal",
  lastName: "name",
  phone: "6505438765",
  country: "US",
  city: "Mountain View",
  address: "1295 Charleston Rd",
};

function withKitesimBillingDefaults(card: KitesimCardProfile, billingEmail: string): KitesimCardProfile {
  return {
    ...card,
    billingEmail: card.billingEmail.trim() || billingEmail,
    firstName: card.firstName.trim() || KITESIM_BILLING_DEFAULTS.firstName,
    lastName: card.lastName.trim() || KITESIM_BILLING_DEFAULTS.lastName,
    phone: card.phone.trim() || KITESIM_BILLING_DEFAULTS.phone,
    country: card.country.trim() || KITESIM_BILLING_DEFAULTS.country,
    city: card.city.trim() || KITESIM_BILLING_DEFAULTS.city,
    address: card.address.trim() || KITESIM_BILLING_DEFAULTS.address,
  };
}

const KITESIM_TASK_META: Record<
  KitesimTaskStatus,
  { color: "blue" | "green" | "grey" | "orange" | "red"; label: string }
> = {
  idle: { color: "grey", label: "空闲" },
  queued: { color: "blue", label: "已排队" },
  running: { color: "orange", label: "执行中" },
  succeeded: { color: "green", label: "成功" },
  failed: { color: "red", label: "失败" },
};

const KITESIM_OPERATION_META: Record<
  KitesimOperationStatus,
  { color: "blue" | "green" | "grey" | "orange" | "red" | "violet"; label: string }
> = {
  queued: { color: "blue", label: "已排队" },
  running: { color: "orange", label: "执行中" },
  succeeded: { color: "green", label: "成功" },
  failed: { color: "red", label: "失败" },
  uncertain: { color: "orange", label: "结果不确定，勿重试" },
  requires_action: { color: "violet", label: "需 3DS / 人工核对" },
};

function formatKitesimDuration(product: KitesimProduct) {
  const value = product.durationValue || 1;
  if (product.durationType === 1) return `${value} 个月`;
  if (product.durationType === 2) return `${value} 个季度`;
  if (product.durationType === 3) return value === 1 ? "半年" : `${value} 个半年周期`;
  return `${value} / ${product.durationType}`;
}

function formatKitesimMoney(currency: string | undefined, value: string | undefined) {
	return [currency, formatPoints(value)].filter(Boolean).join(" ");
}

function hasPendingKitesimReconcile(operation: KitesimOperation) {
	if (!operation.reconcileRequestedAt) return false;
	const requestedAt = Date.parse(operation.reconcileRequestedAt);
	const reconciledAt = operation.lastReconciledAt ? Date.parse(operation.lastReconciledAt) : 0;
	return Number.isFinite(requestedAt) && requestedAt > reconciledAt;
}

function KitesimUpstreamSection({
  canSensitive,
  canWrite,
}: Pick<SectionProps, "canSensitive" | "canWrite">) {
  const { t } = useTranslation();
  const [upstream, setUpstream] = useState<KitesimUpstreamView | null>(null);
  const [accountId, setAccountId] = useState(0);
  const [card, setCard] = useState<KitesimCardProfile>(EMPTY_KITESIM_CARD);
  const [purchaseProductId, setPurchaseProductId] = useState(0);
  const [purchaseCount, setPurchaseCount] = useState(1);
  const [rechargeAmount, setRechargeAmount] = useState("");
  const [rechargeCVC, setRechargeCVC] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
	const [purchasing, setPurchasing] = useState(false);
  const [recharging, setRecharging] = useState(false);
	const [operationAction, setOperationAction] = useState("");
  const requestRef = useRef<AbortController | null>(null);
  const accountEditedRef = useRef(false);

  const load = useCallback(async (background = false) => {
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    if (!background) setLoading(true);
    try {
      const next = await getKitesimUpstream(controller.signal);
      if (controller.signal.aborted) return;
      setUpstream(next);
      if (!accountEditedRef.current) setAccountId(next.accountId || 0);
      setPurchaseProductId((current) => (
        next.products.some((product) => product.id === current && product.active)
          ? current
          : next.products.find((product) => product.active)?.id ?? 0
      ));
    } catch (error) {
      if (!controller.signal.aborted && !background) {
        Toast.error(getIamErrorMessage(t, error, "Kitesim upstream load failed."));
      }
    } finally {
      if (requestRef.current === controller) {
        requestRef.current = null;
        setLoading(false);
      }
    }
  }, [t]);

  useEffect(() => {
    void load();
    return () => requestRef.current?.abort();
  }, [load]);

  const hasPendingTask = Boolean(
    upstream?.refreshStatus === "queued" ||
    upstream?.refreshStatus === "running" ||
		upstream?.operations.some((operation) => (
			operation.status === "queued" || operation.status === "running" ||
			hasPendingKitesimReconcile(operation)
		)),
  );

  useEffect(() => {
    if (!hasPendingTask) return undefined;
    const timer = window.setInterval(() => void load(true), 3000);
    return () => window.clearInterval(timer);
  }, [hasPendingTask, load]);

  const activeProducts = useMemo(
    () => upstream?.products.filter((product) => product.active) ?? [],
    [upstream?.products],
  );
  const selectedProduct = activeProducts.find((product) => product.id === purchaseProductId);
  const selectedAccountEmail = useMemo(
    () => upstream?.accounts.find((account) => account.id === accountId)?.account
      ?? (accountId === upstream?.accountId ? upstream?.account ?? "" : ""),
    [accountId, upstream?.account, upstream?.accountId, upstream?.accounts],
  );
  const cardDraft = useMemo(
    () => withKitesimBillingDefaults(card, selectedAccountEmail),
    [card, selectedAccountEmail],
  );
  const settingsDirty = accountId !== (upstream?.accountId ?? 0);
  const cardTouched = Boolean(
    card.number.trim() || card.expiryMonth || card.expiryYear || card.holder.trim(),
  );
  const cardComplete = Boolean(
    cardDraft.number.trim() && cardDraft.expiryMonth && cardDraft.expiryYear && cardDraft.holder.trim() &&
    cardDraft.billingEmail.trim() && cardDraft.firstName.trim() && cardDraft.lastName.trim() &&
    cardDraft.phone.trim() && cardDraft.country.trim() && cardDraft.city.trim() && cardDraft.address.trim(),
  );

  const saveKitesimSettings = async () => {
    if (!accountId) {
      Toast.warning("请选择系统 Kitesim 账号。");
      return;
    }
    if (cardTouched && !cardComplete) {
      Toast.warning("请完整填写信用卡和账单资料，或全部留空以保留原卡。");
      return;
    }
    setSaving(true);
    try {
      await updateKitesimUpstream({
        accountId,
        clearCard: false,
        ...(canSensitive && cardComplete ? { card: cardDraft } : {}),
      });
      accountEditedRef.current = false;
      setCard({ ...EMPTY_KITESIM_CARD });
      Toast.success("Kitesim 上游设置已保存。");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Kitesim upstream save failed."));
    } finally {
      setSaving(false);
    }
  };

  const clearKitesimCard = async () => {
    if (!upstream?.accountId) return;
    setSaving(true);
    try {
      await updateKitesimUpstream({ accountId: upstream.accountId, clearCard: true });
      setCard({ ...EMPTY_KITESIM_CARD });
      Toast.success("Kitesim 信用卡已清除。");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Kitesim upstream save failed."));
      throw error;
    } finally {
      setSaving(false);
    }
  };

  const queueKitesimRefresh = async () => {
    setRefreshing(true);
    try {
      await refreshKitesimUpstream();
      Toast.success("已提交 Kitesim 余额和产品同步任务。");
      await load(true);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Kitesim upstream refresh failed."));
    } finally {
      setRefreshing(false);
    }
  };

  const queuePurchase = async () => {
    if (!selectedProduct || purchaseCount < 1 || purchaseCount > 100) {
      Toast.warning("请选择补号套餐，并填写 1 到 100 的数量。");
      return;
    }
    setPurchasing(true);
    try {
      const operation = await purchaseKitesimNumbers(
        selectedProduct.id,
        purchaseCount,
        selectedProduct.buyPrice,
      );
      Toast.success(`补号任务 #${operation.id} 已提交。`);
      await load(true);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Kitesim purchase failed."));
    } finally {
      setPurchasing(false);
    }
  };

	const queueRecharge = async () => {
    const amount = Number(rechargeAmount);
    if (!Number.isFinite(amount) || amount <= 0 || amount > 10000 || !/^\d{3,4}$/.test(rechargeCVC.trim())) {
      Toast.warning("请输入大于 0 且不超过 10000 的充值金额和 3 至 4 位 CVC。");
      return;
    }
    setRecharging(true);
    try {
      const operation = await rechargeKitesimAccount(rechargeAmount.trim(), rechargeCVC.trim());
      Toast.success(`充值任务 #${operation.id} 已提交。`);
      await load(true);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Kitesim recharge failed."));
    } finally {
      setRechargeCVC("");
      setRecharging(false);
    }
	};

	const reconcileOperation = async (operation: KitesimOperation) => {
		const action = `reconcile:${operation.id}`;
		setOperationAction(action);
		try {
			await reconcileKitesimOperation(operation.id);
			Toast.success(`Kitesim 操作 #${operation.id} 已提交只读对账。`);
			await load(true);
		} catch (error) {
			Toast.error(getIamErrorMessage(t, error, "Kitesim reconciliation failed."));
		} finally {
			setOperationAction("");
		}
	};

	const resolveOperation = (operation: KitesimOperation, outcome: "succeeded" | "failed") => {
		const success = outcome === "succeeded";
		Modal.confirm({
			title: success ? "人工标记操作成功" : "人工标记操作失败",
			content: "仅在已经核对 Kitesim 订单、账号余额和银行卡账单后继续。此操作不会重放任何付款请求。",
			okButtonProps: { type: success ? "primary" : "danger" },
			onOk: async () => {
				const action = `${outcome}:${operation.id}`;
				setOperationAction(action);
				try {
					await resolveKitesimOperation(operation.id, {
						outcome,
						note: success
							? "管理员已核对 Kitesim 订单、账号余额和银行卡账单，确认操作成功。"
							: "管理员已核对 Kitesim 订单、账号余额和银行卡账单，确认操作失败。",
					});
					Toast.success(`Kitesim 操作 #${operation.id} 已标记为${success ? "成功" : "失败"}。`);
					await load(true);
				} catch (error) {
					Toast.error(getIamErrorMessage(t, error, "Kitesim resolution failed."));
					throw error;
				} finally {
					setOperationAction("");
				}
			},
		});
	};

  const refreshMeta = KITESIM_TASK_META[upstream?.refreshStatus ?? "idle"];
  const cardSummary = upstream?.cardConfigured
    ? `${upstream.cardBrand || "Card"} •••• ${upstream.cardLast4 || "-"} · ${String(upstream.cardExpiryMonth || 0).padStart(2, "0")}/${upstream.cardExpiryYear || "-"}`
    : "未配置";

  return (
    <SettingsSection title={<SettingsCardHeader icon={<Smartphone size={16} />} title="Kitesim 上游" description="Kitesim 号码供应、结算与套餐目录" />}>
      <Spin spinning={loading}>
        <div className="mb-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
          <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">账户余额</Text><div className="mt-1 text-xl font-semibold">{formatPoints(upstream?.balance)}</div></div>
          <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">系统账号</Text><div className="mt-1 truncate font-medium">{upstream?.account || "未选择"}</div></div>
          <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">信用卡</Text><div className="mt-1 truncate font-medium">{cardSummary}</div></div>
          <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">同步状态</Text><div className="mt-2"><Tag color={refreshMeta.color} shape="circle">{refreshMeta.label}</Tag></div></div>
          <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">余额更新时间</Text><div className="mt-1 font-medium">{formatTime(upstream?.balanceUpdatedAt)}</div></div>
        </div>

        {upstream?.lastSafeError ? <div className="mb-4 rounded-lg bg-[var(--semi-color-danger-light-default)] px-3 py-2 text-sm text-[var(--semi-color-danger)]">{upstream.lastSafeError}</div> : null}

        <div className="border-t border-[var(--semi-color-border)] pt-5">
          <div className="mb-4 flex items-center gap-2 font-medium"><CreditCard size={15} />系统账号与信用卡</div>
          <SettingsFormGrid>
            <div>
              <div className="mb-2 text-sm font-medium">系统 Kitesim 账号</div>
              <Select
                aria-label="系统 Kitesim 账号"
                disabled={!canWrite}
                emptyContent="请先在管理员 Kitesim 页面导入账号"
                filter
                onChange={(value) => {
                  const nextAccountId = Number(value ?? 0);
                  accountEditedRef.current = nextAccountId !== (upstream?.accountId ?? 0);
                  setAccountId(nextAccountId);
                }}
                optionList={(upstream?.accounts ?? []).map((account) => ({
                  label: `${account.account} · ${account.tokenAvailable ? "Token 可用" : "Token 缺失"} · ${KITESIM_TASK_META[account.syncStatus].label}`,
                  value: account.id,
                }))}
                placeholder="选择系统账号"
                style={{ width: "100%" }}
                value={accountId || undefined}
              />
            </div>
          </SettingsFormGrid>

          {canSensitive ? (
            <SettingsAccessBoundary canWrite={canWrite}>
              <SettingsFormGrid className="mt-4">
                <SettingsTextField label="卡号" value={card.number} onChange={(value) => setCard((current) => ({ ...current, number: value }))} type="password" disabled={!canWrite} placeholder={upstream?.cardConfigured ? "留空保持当前信用卡" : "信用卡号"} />
                <SettingsNumberField label="到期月份" value={card.expiryMonth || undefined} onChange={(value) => setCard((current) => ({ ...current, expiryMonth: value }))} min={1} max={12} precision={0} />
                <SettingsNumberField label="到期年份" value={card.expiryYear || undefined} onChange={(value) => setCard((current) => ({ ...current, expiryYear: value }))} min={new Date().getFullYear()} max={2200} precision={0} />
                <SettingsTextField label="持卡人姓名" value={card.holder} onChange={(value) => setCard((current) => ({ ...current, holder: value }))} disabled={!canWrite} />
                <SettingsTextField label="账单邮箱" value={cardDraft.billingEmail} onChange={(value) => setCard((current) => ({ ...current, billingEmail: value }))} disabled={!canWrite} />
                <SettingsTextField label="账单名" value={cardDraft.firstName} onChange={(value) => setCard((current) => ({ ...current, firstName: value }))} disabled={!canWrite} />
                <SettingsTextField label="账单姓" value={cardDraft.lastName} onChange={(value) => setCard((current) => ({ ...current, lastName: value }))} disabled={!canWrite} />
                <SettingsTextField label="账单手机号" value={cardDraft.phone} onChange={(value) => setCard((current) => ({ ...current, phone: value }))} disabled={!canWrite} />
                <SettingsTextField label="账单国家（ISO 2 位）" value={cardDraft.country} onChange={(value) => setCard((current) => ({ ...current, country: value.toUpperCase() }))} disabled={!canWrite} placeholder="US" />
                <SettingsTextField label="账单城市" value={cardDraft.city} onChange={(value) => setCard((current) => ({ ...current, city: value }))} disabled={!canWrite} />
                <SettingsTextField label="账单地址" value={cardDraft.address} onChange={(value) => setCard((current) => ({ ...current, address: value }))} disabled={!canWrite} />
              </SettingsFormGrid>
            </SettingsAccessBoundary>
          ) : (
            <div className="mt-4 text-sm text-[var(--semi-color-text-2)]">需要敏感设置权限才能修改信用卡和发起充值。</div>
          )}

          <div className="mt-5 flex flex-wrap gap-2">
            <Button disabled={!canWrite} icon={<Save size={14} />} loading={saving} onClick={() => void saveKitesimSettings()} theme="solid" type="primary">保存 Kitesim 设置</Button>
            <Button disabled={!canWrite || !upstream?.accountId || settingsDirty} icon={<RefreshCw size={14} />} loading={refreshing} onClick={() => void queueKitesimRefresh()} type="tertiary">同步余额和产品</Button>
            {upstream?.cardConfigured && canSensitive ? <Button disabled={!canWrite || settingsDirty} icon={<Trash2 size={14} />} onClick={() => Modal.confirm({ title: "清除 Kitesim 信用卡", content: "确认清除已保存的信用卡吗？", okButtonProps: { type: "danger" }, onOk: clearKitesimCard })} type="danger">清除信用卡</Button> : null}
          </div>
        </div>

        <div className="mt-6 grid gap-6 border-t border-[var(--semi-color-border)] pt-5 lg:grid-cols-2 lg:divide-x lg:divide-[var(--semi-color-border)]">
          <div className="min-w-0 lg:pr-6">
            <div className="mb-4 flex items-center gap-2 font-medium"><ShoppingCart size={15} />补充号码</div>
            <SettingsAccessBoundary canWrite={canWrite && !settingsDirty}>
              <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_150px]">
                <div>
                  <div className="mb-2 text-sm font-medium">产品套餐</div>
                  <Select
                    aria-label="Kitesim 补号套餐"
                    disabled={!canWrite || settingsDirty}
                    emptyContent="请先同步上游产品"
                    filter
                    onChange={(value) => setPurchaseProductId(Number(value ?? 0))}
                    optionList={activeProducts.map((product) => ({
                      label: `${product.countryCode} · ${formatKitesimDuration(product)} · ${formatKitesimMoney(product.currency, product.buyPrice)}`,
                      value: product.id,
                    }))}
                    style={{ width: "100%" }}
                    value={purchaseProductId || undefined}
                  />
                </div>
                <SettingsNumberField label="补号数量" value={purchaseCount} onChange={setPurchaseCount} min={1} max={100} precision={0} />
              </div>
            </SettingsAccessBoundary>
            <Button className="mt-4" disabled={!canWrite || settingsDirty || !upstream?.accountId || !selectedProduct} loading={purchasing} onClick={() => void queuePurchase()} theme="solid" type="primary">提交补号</Button>
          </div>

          <div className="min-w-0 lg:pl-6">
            <div className="mb-4 flex items-center gap-2 font-medium"><WalletCards size={15} />快捷充值</div>
            <SettingsFormGrid className="xl:grid-cols-2">
				<SettingsTextField label="充值金额（HKD）" value={rechargeAmount} onChange={setRechargeAmount} disabled={!canWrite || !canSensitive} placeholder="10.00" />
              <SettingsTextField label="CVC（仅本次使用）" value={rechargeCVC} onChange={setRechargeCVC} disabled={!canWrite || !canSensitive} type="password" placeholder="123" />
            </SettingsFormGrid>
            <Button className="mt-4" disabled={!canWrite || !canSensitive || settingsDirty || !upstream?.accountId || !upstream.cardConfigured} loading={recharging} onClick={() => void queueRecharge()} theme="solid" type="primary">提交充值</Button>
          </div>
        </div>

        <div className="mt-6 border-t border-[var(--semi-color-border)] pt-5">
          <div className="mb-4 flex items-center gap-2 font-medium"><ShoppingCart size={15} />上游产品价格</div>
          <Table
            columns={[
              { title: "国家", dataIndex: "countryCode", width: 90 },
              { title: "周期", width: 120, render: (_value, product: KitesimProduct) => formatKitesimDuration(product) },
              { title: "套餐 ID", dataIndex: "packageId", width: 180 },
              { title: "购买价", width: 130, render: (_value, product: KitesimProduct) => formatKitesimMoney(product.currency, product.buyPrice) },
              { title: "原价", width: 130, render: (_value, product: KitesimProduct) => formatKitesimMoney(product.currency, product.originalPrice) },
              { title: "续费价", width: 130, render: (_value, product: KitesimProduct) => formatKitesimMoney(product.currency, product.autoRenewPrice) },
              { title: "状态", dataIndex: "active", width: 90, render: (value) => <Tag color={value ? "green" : "grey"} shape="circle">{value ? "可用" : "已下线"}</Tag> },
              { title: "最近同步", dataIndex: "lastSeenAt", width: 180, render: (value) => formatTime(String(value ?? "")) },
            ]}
            dataSource={upstream?.products ?? []}
            empty={<div className="py-10 text-center text-[var(--semi-color-text-2)]">暂无 Kitesim 产品价格</div>}
            pagination={{ pageSize: 15 }}
            rowKey="id"
            scroll={{ x: 1050 }}
          />
        </div>

        <div className="mt-6 border-t border-[var(--semi-color-border)] pt-5">
          <div className="mb-4 flex items-center gap-2 font-medium"><History size={15} />最近操作</div>
          <Table
            columns={[
              { title: "任务", width: 120, render: (_value, operation: KitesimOperation) => ({ purchase: "补号", recharge: "充值", renew: "续期" })[operation.kind] },
              { title: "账号 / 号码", width: 220, render: (_value, operation: KitesimOperation) => <div><div className="font-medium">{operation.phoneNumber || operation.account || "-"}</div><Text type="tertiary" size="small">{operation.countryCode || "-"} · {operation.packageId || "-"}</Text></div> },
				{
					title: t("Amount / unit price ceiling"),
					width: 180,
					render: (_value, operation: KitesimOperation) => {
						const amount = formatKitesimMoney(operation.currency, operation.amount);
						if (operation.kind === "recharge") return amount;
						return operation.kind === "purchase" ? `≤ ${amount} / 号` : `≤ ${amount}`;
					},
				},
              { title: "状态", dataIndex: "status", width: 190, render: (value: KitesimOperationStatus) => { const meta = KITESIM_OPERATION_META[value]; return <Tag color={meta.color} shape="circle">{meta.label}</Tag>; } },
              { title: "上游订单", dataIndex: "providerOrderNos", width: 220, render: (value) => Array.isArray(value) && value.length > 0 ? value.join(", ") : "-" },
              { title: "提交时间", dataIndex: "queuedAt", width: 180, render: (value) => formatTime(String(value ?? "")) },
              { title: "完成时间", dataIndex: "finishedAt", width: 180, render: (value) => formatTime(String(value ?? "")) },
				{ title: "结果", dataIndex: "lastSafeError", width: 260, render: (value) => String(value || "-") },
				{
					title: "操作",
					width: 250,
					fixed: "right",
					render: (_value, operation: KitesimOperation) => {
						const recoverable = operation.status === "uncertain" || operation.status === "requires_action";
						if (!recoverable) return "-";
						return (
							<div className="flex items-center gap-1">
								<Button disabled={!canWrite} icon={<RefreshCw size={13} />} loading={operationAction === `reconcile:${operation.id}`} onClick={() => void reconcileOperation(operation)} size="small" type="tertiary">对账</Button>
								{canSensitive ? <Button disabled={!canWrite} icon={<CheckCircle2 size={13} />} loading={operationAction === `succeeded:${operation.id}`} onClick={() => resolveOperation(operation, "succeeded")} size="small" type="tertiary">成功</Button> : null}
								{canSensitive ? <Button disabled={!canWrite} icon={<XCircle size={13} />} loading={operationAction === `failed:${operation.id}`} onClick={() => resolveOperation(operation, "failed")} size="small" type="danger">失败</Button> : null}
							</div>
						);
					},
				},
            ]}
            dataSource={upstream?.operations ?? []}
            empty={<div className="py-10 text-center text-[var(--semi-color-text-2)]">暂无 Kitesim 操作</div>}
            pagination={false}
            rowKey="id"
				scroll={{ x: 1800 }}
          />
        </div>
      </Spin>
    </SettingsSection>
  );
}

export default function UpstreamsSection({ canSensitive, canWrite }: SectionProps) {
  return <KitesimUpstreamSection canSensitive={canSensitive} canWrite={canWrite} />;
}
