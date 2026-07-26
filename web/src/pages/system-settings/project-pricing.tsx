import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { PackageCheck, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { PROJECT_PRICE_KEYS } from "./payment-billing-keys";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const DEFAULTS = {
  default_project_microsoft_code_price: 0.008,
  default_project_microsoft_code_supplier_price: 0.005,
  default_project_microsoft_purchase_price: 0.01,
  default_project_microsoft_purchase_supplier_price: 0.007,
  default_project_domain_code_price: 0.08,
  default_project_domain_code_supplier_price: 0.04,
  default_project_domain_purchase_price: 0,
  default_project_domain_purchase_supplier_price: 0,
};

export default function ProjectPricingSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(() => parseOption(options, DEFAULTS));
  const [saving, setSaving] = useState(false);
  const update = (key: keyof typeof DEFAULTS, value: number) => setForm((current) => ({ ...current, [key]: value }));
  const field = (label: string, key: keyof typeof DEFAULTS) => (
    <SettingsNumberField label={t(label)} min={0} onChange={(value) => update(key, value)} precision={6} step={0.000001} value={form[key]} />
  );
  const save = async () => {
    setSaving(true);
    try {
      await onBulkSave(PROJECT_PRICE_KEYS.map((key) => ({ key, value: String(form[key]) })));
    } finally {
      setSaving(false);
    }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<PackageCheck size={16} />} title={t("项目商品默认单价")} description={t("新建或审批项目时自动带入，可在项目管理中继续调整")} />}>
    <div className="mt-4 text-sm font-medium text-[var(--semi-color-text-0)]">{t("微软邮箱")}</div>
    <SettingsFormGrid className="mt-3">
      {field("接码价", "default_project_microsoft_code_price")}
      {field("接码结算价", "default_project_microsoft_code_supplier_price")}
      {field("购买价", "default_project_microsoft_purchase_price")}
      {field("购买结算价", "default_project_microsoft_purchase_supplier_price")}
    </SettingsFormGrid>
    <div className="mt-5 text-sm font-medium text-[var(--semi-color-text-0)]">{t("域名邮箱")}</div>
    <SettingsFormGrid className="mt-3">
      {field("接码价", "default_project_domain_code_price")}
      {field("接码结算价", "default_project_domain_code_supplier_price")}
      {field("购买价", "default_project_domain_purchase_price")}
      {field("购买结算价", "default_project_domain_purchase_supplier_price")}
    </SettingsFormGrid>
    <Button className="mt-5" icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary">{t("保存设置")}</Button>
  </SettingsSection>;
}
