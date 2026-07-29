import { useState } from "react";
import { Button, InputNumber, Toast } from "@douyinfe/semi-ui";
import { CreditCard, Plus, RefreshCw, Save, Trash2, WalletCards } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { applyEPayURLDefaults, changeEPayVersion, EPAY_GATEWAY_KEYS, EPAY_WRITE_ONLY_KEYS, RECHARGE_CHECK_KEYS, TOPUP_KEYS } from "./payment-billing-keys";
import { SettingsAccessBoundary, SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection, SettingsSelectField, SettingsTextField, SettingsTextareaField } from "./settings-layout";
import { parseTopupTiers, serializeTopupTiers, type TopupTier } from "./topup-tiers";

const D: Record<string, unknown> = { epay_enabled: false, epay_version: "v1", epay_gateway_url: "", epay_merchant_id: "", epay_merchant_key: "", epay_private_key: "", epay_platform_public_key: "", epay_notify_url: "", epay_return_url: "", points_per_yuan: 1000, min_topup_amount: 10000, topup_fee_rate: 0, topup_fee_cap: 0, topup_amount_presets: "[10000, 20000, 50000, 100000, 200000, 500000]", topup_amount_bonus: "{}", max_pending_recharge_orders: 10, async_check_request_timeout_seconds: 5 };
const EPAY_WRITE_ONLY = new Set<string>(EPAY_WRITE_ONLY_KEYS);

export default function PaymentSection({ options, onBulkSave, canSensitive }: SectionProps) {
  const { t } = useTranslation();
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  const [form, setForm] = useState(() => applyEPayURLDefaults(parseOption(options, D as any) as Record<string, unknown>, origin));
  const [topupTiers, setTopupTiers] = useState(() => parseTopupTiers(form.topup_amount_presets, form.topup_amount_bonus));
  const [savingCard, setSavingCard] = useState<string | null>(null);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const save = async (card: string, keys: string[]) => {
    setSavingCard(card);
    try {
      await onBulkSave(keys.flatMap((key) => {
        const value = String(form[key] ?? "");
        return EPAY_WRITE_ONLY.has(key) && !value.trim() ? [] : [{ key, value }];
      }));
      if (card === "gateway") {
        setForm((current) => ({ ...current, epay_merchant_key: "", epay_private_key: "" }));
      }
    }
    finally { setSavingCard(null); }
  };
  const saveTopup = async () => {
    if (topupTiers.some(({ amount, bonus }) => !Number.isFinite(amount) || amount <= 0 || !Number.isFinite(bonus) || bonus < 0)) {
      Toast.warning(t("充值档位积分必须大于 0，赠送积分不能为负数"));
      return;
    }
    const amounts = topupTiers.map(({ amount }) => amount.toFixed(6));
    if (new Set(amounts).size !== amounts.length) {
      Toast.warning(t("充值档位积分不能重复"));
      return;
    }
    const serialized = serializeTopupTiers(topupTiers);
    setForm((current) => ({ ...current, ...serialized }));
    setSavingCard("topup");
    try {
      await onBulkSave(TOPUP_KEYS.map((key) => ({ key, value: key in serialized ? serialized[key as keyof typeof serialized] : String(form[key] ?? "") })));
    } finally { setSavingCard(null); }
  };
  const updateTier = (index: number, values: Partial<TopupTier>) => setTopupTiers((current) => current.map((tier, tierIndex) => tierIndex === index ? { ...tier, ...values } : tier));
  const addTier = () => setTopupTiers((current) => [...current, { amount: current.length ? Math.max(...current.map(({ amount }) => amount)) + 10000 : 10000, bonus: 0 }]);

  return <div className="space-y-6">
    <SettingsAccessBoundary canWrite={canSensitive}>
      <SettingsSection title={<SettingsCardHeader icon={<CreditCard size={16} />} title={t("支付网关")} description={t("易支付 V1 / V2；回调只确认收到，不参与入账")} enabled={!!form.epay_enabled} onToggle={(value) => update("epay_enabled", value)} statusText={form.epay_enabled ? t("已启用") : t("已禁用")} />}>
        <SettingsFormGrid className="mt-4">
          <SettingsSelectField label={t("易支付版本")} value={String(form.epay_version)} onChange={(value) => setForm((current) => changeEPayVersion(current, value, origin))} options={[{ label: "V1", value: "v1" }, { label: "V2", value: "v2" }]} />
          <SettingsTextField label={t("支付网关地址")} value={String(form.epay_gateway_url)} onChange={(value) => update("epay_gateway_url", value)} placeholder="https://pay.example.com/" />
          <SettingsTextField label={t("商户 ID")} value={String(form.epay_merchant_id)} onChange={(value) => update("epay_merchant_id", value)} />
          {form.epay_version === "v2" ? <>
            <SettingsTextField label={t("商户私钥（V2）")} value={String(form.epay_private_key)} onChange={(value) => update("epay_private_key", value)} type="password" placeholder={t("粘贴易支付生成的商户私钥（Base64 或 PEM，保存后不回显）")} />
            <SettingsTextareaField label={t("平台公钥（V2）")} value={String(form.epay_platform_public_key)} onChange={(value) => update("epay_platform_public_key", value)} rows={4} placeholder={t("粘贴易支付商户中心显示的平台公钥，不是商户公钥")} />
          </> : <SettingsTextField label={t("商户 MD5 密钥（V1）")} value={String(form.epay_merchant_key)} onChange={(value) => update("epay_merchant_key", value)} type="password" placeholder={t("已保存密钥不会回显；留空保持不变")} />}
          <SettingsTextField label={t("支付回调地址")} value={String(form.epay_notify_url)} onChange={(value) => update("epay_notify_url", value)} />
          <SettingsTextField label={t("支付同步跳转地址")} value={String(form.epay_return_url)} onChange={(value) => update("epay_return_url", value)} />
        </SettingsFormGrid>
        <Button icon={<Save size={14} />} loading={savingCard === "gateway"} onClick={() => void save("gateway", [...EPAY_GATEWAY_KEYS]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
      </SettingsSection>
    </SettingsAccessBoundary>

    <SettingsSection title={<SettingsCardHeader icon={<WalletCards size={16} />} title={t("充值配置")} description={t("配置支付宝积分汇率、最低充值积分、手续费和前端充值档位")} />}>
      <SettingsFormGrid className="mt-4">
        <SettingsNumberField label={t("支付宝汇率（1 人民币等于多少积分）")} description={t("仅创建支付宝订单时用于把积分换算为人民币")} value={number(form.points_per_yuan)} onChange={(value) => update("points_per_yuan", value)} min={0.000001} precision={6} step={1} />
        <SettingsNumberField label={t("最低充值积分")} value={number(form.min_topup_amount)} onChange={(value) => update("min_topup_amount", value)} min={0.000001} precision={6} step={0.01} />
        <SettingsNumberField label={t("充值手续费率（%）")} description={t("千分之六请输入 0.6")} value={number(form.topup_fee_rate)} onChange={(value) => update("topup_fee_rate", value)} min={0} max={100} precision={6} step={0.1} />
        <SettingsNumberField label={t("手续费封顶积分")} description={t("0 表示不封顶")} value={number(form.topup_fee_cap)} onChange={(value) => update("topup_fee_cap", value)} min={0} precision={6} step={0.01} />
        <SettingsNumberField label={t("单用户最大未支付充值订单数")} value={number(form.max_pending_recharge_orders)} onChange={(value) => update("max_pending_recharge_orders", value)} min={1} max={100} precision={0} />
      </SettingsFormGrid>
      <div className="mt-5 overflow-hidden rounded-lg border border-[var(--semi-color-border)]">
        <div className="flex items-center justify-between gap-4 bg-[var(--semi-color-fill-0)] px-4 py-3">
          <div>
            <div className="text-sm font-medium text-[var(--semi-color-text-0)]">{t("充值档位")}</div>
            <div className="mt-0.5 text-xs text-[var(--semi-color-text-2)]">{t("逐项设置前端积分和对应赠送积分，赠送为 0 表示不赠送")}</div>
          </div>
          <Button icon={<Plus size={14} />} onClick={addTier} size="small">{t("添加档位")}</Button>
        </div>
        <div className="hidden grid-cols-[1fr_1fr_32px] gap-4 border-t border-[var(--semi-color-border)] px-4 py-2 text-xs text-[var(--semi-color-text-2)] sm:grid">
          <span>{t("充值积分")}</span>
          <span>{t("赠送积分")}</span>
          <span />
        </div>
        {topupTiers.length ? topupTiers.map((tier, index) => (
          <div key={index} className="grid gap-3 border-t border-[var(--semi-color-border)] px-4 py-3 sm:grid-cols-[1fr_1fr_32px] sm:items-center sm:gap-4">
            <label className="min-w-0">
              <span className="mb-1.5 block text-xs text-[var(--semi-color-text-2)] sm:hidden">{t("充值积分")}</span>
              <InputNumber aria-label={t("充值积分")} min={0.000001} onNumberChange={(value) => updateTier(index, { amount: Number(value) || 0 })} precision={6} style={{ width: "100%" }} value={tier.amount || ""} />
            </label>
            <label className="min-w-0">
              <span className="mb-1.5 block text-xs text-[var(--semi-color-text-2)] sm:hidden">{t("赠送积分")}</span>
              <InputNumber aria-label={t("赠送积分")} min={0} onNumberChange={(value) => updateTier(index, { bonus: Number(value) || 0 })} precision={6} style={{ width: "100%" }} value={tier.bonus} />
            </label>
            <Button aria-label={t("删除档位")} icon={<Trash2 size={14} />} onClick={() => setTopupTiers((current) => current.filter((_, tierIndex) => tierIndex !== index))} size="small" theme="borderless" type="danger" />
          </div>
        )) : <div className="border-t border-[var(--semi-color-border)] px-4 py-8 text-center text-sm text-[var(--semi-color-text-2)]">{t("暂无充值档位，请添加档位")}</div>}
      </div>
      <Button icon={<Save size={14} />} loading={savingCard === "topup"} onClick={() => void saveTopup().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader icon={<RefreshCw size={16} />} title={t("异步查账")} description={t("回调后前 10 次每 5 秒查账，随后每 30 秒；无回调时 60 秒启动，5 分钟截止")} />}>
      <SettingsFormGrid className="mt-4">
        <SettingsNumberField label={t("单次查账请求超时（秒）")} value={number(form.async_check_request_timeout_seconds)} onChange={(value) => update("async_check_request_timeout_seconds", value)} min={1} max={30} precision={0} />
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "check"} onClick={() => void save("check", [...RECHARGE_CHECK_KEYS]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>
  </div>;
}
