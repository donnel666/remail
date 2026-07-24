import { useState } from "react";
import { Button, InputNumber, Toast } from "@douyinfe/semi-ui";
import { CreditCard, Plus, RefreshCw, Save, Trash2, WalletCards } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection, SettingsSelectField, SettingsTextField } from "./settings-layout";
import { parseTopupTiers, serializeTopupTiers, type TopupTier } from "./topup-tiers";

const D: Record<string, unknown> = { epay_version: "v1", epay_gateway_url: "", epay_merchant_id: "", epay_merchant_key: "", epay_notify_url: "", epay_return_url: "", epay_custom_callback_domain: "", min_topup_amount: 10, topup_fee_rate: 0, topup_fee_cap: 0, topup_amount_presets: "[10, 20, 50, 100, 200, 500]", topup_amount_bonus: "{}", async_check_enabled: true, async_check_poll_interval_seconds: 30, async_check_max_retries: 10, async_check_timeout_minutes: 30, async_check_request_timeout_seconds: 5 };
const GATEWAY_KEYS = ["epay_version", "epay_gateway_url", "epay_merchant_id", "epay_merchant_key", "epay_notify_url", "epay_return_url", "epay_custom_callback_domain"];
const TOPUP_KEYS = ["min_topup_amount", "topup_fee_rate", "topup_fee_cap", "topup_amount_presets", "topup_amount_bonus"];
const CHECK_KEYS = ["async_check_enabled", "async_check_poll_interval_seconds", "async_check_max_retries", "async_check_timeout_minutes", "async_check_request_timeout_seconds"];

export default function PaymentSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D as any) as Record<string, unknown>);
  const [topupTiers, setTopupTiers] = useState(() => parseTopupTiers(form.topup_amount_presets, form.topup_amount_bonus));
  const [savingCard, setSavingCard] = useState<string | null>(null);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const field = (label: string, key: string) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={0} />;
  const save = async (card: string, keys: string[]) => {
    setSavingCard(card);
    try { await onBulkSave(keys.map((key) => ({ key, value: String(form[key] ?? "") }))); }
    finally { setSavingCard(null); }
  };
  const saveTopup = async () => {
    if (topupTiers.some(({ amount, bonus }) => !Number.isFinite(amount) || amount <= 0 || !Number.isFinite(bonus) || bonus < 0)) {
      Toast.warning(t("充值档位金额必须大于 0，赠送金额不能为负数"));
      return;
    }
    const amounts = topupTiers.map(({ amount }) => amount.toFixed(2));
    if (new Set(amounts).size !== amounts.length) {
      Toast.warning(t("充值档位金额不能重复"));
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
  const addTier = () => setTopupTiers((current) => [...current, { amount: current.length ? Math.max(...current.map(({ amount }) => amount)) + 10 : 10, bonus: 0 }]);

  return <div className="space-y-6">
    <SettingsSection title={<SettingsCardHeader icon={<CreditCard size={16} />} title={t("支付网关")} description={t("配置易支付 V1 / V2 协议、商户凭据和回调地址")} />}>
      <SettingsFormGrid className="mt-4">
        <SettingsSelectField label={t("易支付版本")} value={String(form.epay_version)} onChange={(value) => update("epay_version", value)} options={[{ label: "V1", value: "v1" }, { label: "V2", value: "v2" }]} />
        <SettingsTextField label={t("支付网关地址")} value={String(form.epay_gateway_url)} onChange={(value) => update("epay_gateway_url", value)} placeholder="https://pay.example.com/" />
        <SettingsTextField label={t("商户 ID")} value={String(form.epay_merchant_id)} onChange={(value) => update("epay_merchant_id", value)} />
        <SettingsTextField label={t("商户密钥")} value={String(form.epay_merchant_key)} onChange={(value) => update("epay_merchant_key", value)} type="password" />
        <SettingsTextField label={t("支付回调地址")} value={String(form.epay_notify_url)} onChange={(value) => update("epay_notify_url", value)} placeholder="https://example.com/api/callback" />
        <SettingsTextField label={t("支付同步跳转地址")} value={String(form.epay_return_url)} onChange={(value) => update("epay_return_url", value)} />
        <SettingsTextField label={t("自定义回调域名")} value={String(form.epay_custom_callback_domain)} onChange={(value) => update("epay_custom_callback_domain", value)} />
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "gateway"} onClick={() => void save("gateway", GATEWAY_KEYS)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader icon={<WalletCards size={16} />} title={t("充值配置")} description={t("配置最低充值额度、手续费和前端充值档位")} />}>
      <SettingsFormGrid className="mt-4">
        {field("最低充值金额", "min_topup_amount")}
        {field("充值手续费率", "topup_fee_rate")}
        {field("手续费封顶金额", "topup_fee_cap")}
      </SettingsFormGrid>
      <div className="mt-5 overflow-hidden rounded-lg border border-[var(--semi-color-border)]">
        <div className="flex items-center justify-between gap-4 bg-[var(--semi-color-fill-0)] px-4 py-3">
          <div>
            <div className="text-sm font-medium text-[var(--semi-color-text-0)]">{t("充值档位")}</div>
            <div className="mt-0.5 text-xs text-[var(--semi-color-text-2)]">{t("逐项设置前端面额和对应赠送金额，赠送为 0 表示不赠送")}</div>
          </div>
          <Button icon={<Plus size={14} />} onClick={addTier} size="small">{t("添加档位")}</Button>
        </div>
        <div className="hidden grid-cols-[1fr_1fr_32px] gap-4 border-t border-[var(--semi-color-border)] px-4 py-2 text-xs text-[var(--semi-color-text-2)] sm:grid">
          <span>{t("充值金额")}</span>
          <span>{t("赠送金额")}</span>
          <span />
        </div>
        {topupTiers.length ? topupTiers.map((tier, index) => (
          <div key={index} className="grid gap-3 border-t border-[var(--semi-color-border)] px-4 py-3 sm:grid-cols-[1fr_1fr_32px] sm:items-center sm:gap-4">
            <label className="min-w-0">
              <span className="mb-1.5 block text-xs text-[var(--semi-color-text-2)] sm:hidden">{t("充值金额")}</span>
              <InputNumber aria-label={t("充值金额")} min={0.01} onNumberChange={(value) => updateTier(index, { amount: Number(value) || 0 })} precision={2} prefix="¥" style={{ width: "100%" }} value={tier.amount || ""} />
            </label>
            <label className="min-w-0">
              <span className="mb-1.5 block text-xs text-[var(--semi-color-text-2)] sm:hidden">{t("赠送金额")}</span>
              <InputNumber aria-label={t("赠送金额")} min={0} onNumberChange={(value) => updateTier(index, { bonus: Number(value) || 0 })} precision={2} prefix="¥" style={{ width: "100%" }} value={tier.bonus} />
            </label>
            <Button aria-label={t("删除档位")} icon={<Trash2 size={14} />} onClick={() => setTopupTiers((current) => current.filter((_, tierIndex) => tierIndex !== index))} size="small" theme="borderless" type="danger" />
          </div>
        )) : <div className="border-t border-[var(--semi-color-border)] px-4 py-8 text-center text-sm text-[var(--semi-color-text-2)]">{t("暂无充值档位，请添加档位")}</div>}
      </div>
      <Button icon={<Save size={14} />} loading={savingCard === "topup"} onClick={() => void saveTopup()} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader icon={<RefreshCw size={16} />} title={t("异步查账")} description={t("后台主动轮询支付网关，处理 pending 状态充值单")} enabled={!!form.async_check_enabled} onToggle={(value) => update("async_check_enabled", value)} statusText={form.async_check_enabled ? t("已启用") : t("已禁用")} />}>
      <SettingsFormGrid className="mt-4">
        {field("查账轮询间隔（秒）", "async_check_poll_interval_seconds")}
        {field("查账最大重试次数", "async_check_max_retries")}
        {field("查账超时时间（分钟）", "async_check_timeout_minutes")}
        {field("单次查账请求超时（秒）", "async_check_request_timeout_seconds")}
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "check"} onClick={() => void save("check", CHECK_KEYS)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>
  </div>;
}
