import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { BadgePercent, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import OrderSection from "./orders-trade";
import PaymentSection from "./payment-billing";
import { PRODUCT_PRICE_MULTIPLIER_KEYS } from "./payment-billing-keys";
import ProjectPricingSection from "./project-pricing";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const PRODUCT_MULTIPLIERS = Object.fromEntries(
  PRODUCT_PRICE_MULTIPLIER_KEYS.map((key) => [key, 1]),
) as Record<(typeof PRODUCT_PRICE_MULTIPLIER_KEYS)[number], number>;

const MULTIPLIER_FIELDS = [
  [PRODUCT_PRICE_MULTIPLIER_KEYS[0], "Outlook 倍率"],
  [PRODUCT_PRICE_MULTIPLIER_KEYS[1], "Gmail 倍率"],
  [PRODUCT_PRICE_MULTIPLIER_KEYS[2], "iCloud 倍率"],
  [PRODUCT_PRICE_MULTIPLIER_KEYS[3], "域名邮箱倍率"],
] as const;

function ProductMultiplierSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(() => parseOption(options, PRODUCT_MULTIPLIERS));
  const [saving, setSaving] = useState(false);
  const save = async () => {
    setSaving(true);
    try {
      await onBulkSave(MULTIPLIER_FIELDS.map(([key]) => ({ key, value: String(form[key]) })));
    } finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<BadgePercent size={16} />} title={t("商品促销倍率")} description={t("结算采用商品倍率与用户分组倍率中的较低值；1 为原价")} />}>
    <SettingsFormGrid className="mt-4">
      {MULTIPLIER_FIELDS.map(([key, label]) => <SettingsNumberField key={key} label={t(label)} value={form[key]} onChange={(value) => setForm((current) => ({ ...current, [key]: value }))} min={0} max={1} precision={6} step={0.01} />)}
    </SettingsFormGrid>
    <Button className="mt-5" icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary">{t("保存设置")}</Button>
  </SettingsSection>;
}

export default function OrdersPaymentSection(props: SectionProps) {
  return <div className="space-y-6">
    <OrderSection {...props} />
    <ProjectPricingSection {...props} />
    <ProductMultiplierSection {...props} />
    <PaymentSection {...props} />
  </div>;
}
