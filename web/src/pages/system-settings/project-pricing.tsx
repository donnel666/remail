import { useState } from "react";
import { Button, Toast } from "@douyinfe/semi-ui";
import { PackageCheck, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { PROJECT_PRICE_KEYS, PROJECT_SERVICE_KEYS } from "./payment-billing-keys";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection, SettingsSwitchField } from "./settings-layout";

const DEFAULTS = {
  default_project_microsoft_code_price: 8,
  default_project_microsoft_code_supplier_price: 5,
  default_project_microsoft_purchase_price: 10,
  default_project_microsoft_purchase_supplier_price: 7,
  default_project_microsoft_code_enabled: true,
  default_project_microsoft_purchase_enabled: true,
  default_project_domain_code_price: 80,
  default_project_domain_code_supplier_price: 40,
  default_project_domain_purchase_price: 0,
  default_project_domain_purchase_supplier_price: 0,
  default_project_domain_code_enabled: true,
  default_project_domain_purchase_enabled: false,
  default_project_gmail_code_price: 8,
  default_project_gmail_code_supplier_price: 0,
  default_project_gmail_purchase_price: 0,
  default_project_gmail_purchase_supplier_price: 0,
  default_project_gmail_code_enabled: true,
  default_project_gmail_purchase_enabled: false,
  default_project_gmail_variant_code_price: 8,
  default_project_gmail_variant_code_supplier_price: 0,
  default_project_gmail_variant_purchase_price: 0,
  default_project_gmail_variant_purchase_supplier_price: 0,
  default_project_gmail_variant_code_enabled: true,
  default_project_gmail_variant_purchase_enabled: false,
  default_project_icloud_code_price: 8,
  default_project_icloud_code_supplier_price: 5,
  default_project_icloud_purchase_price: 10,
  default_project_icloud_purchase_supplier_price: 7,
  default_project_icloud_code_enabled: true,
  default_project_icloud_purchase_enabled: true,
};

const PRODUCT_TYPES = ["microsoft", "domain", "gmail", "gmail_variant", "icloud"] as const;

export default function ProjectPricingSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(() => parseOption(options, DEFAULTS));
  const [saving, setSaving] = useState(false);
  const update = (key: keyof typeof DEFAULTS, value: boolean | number) => setForm((current) => ({ ...current, [key]: value }));
  const field = (label: string, key: (typeof PROJECT_PRICE_KEYS)[number]) => (
    <SettingsNumberField label={`${t(label)}（${t("Points")}）`} min={0} onChange={(value) => update(key, value)} precision={6} step={0.01} value={form[key]} />
  );
  const toggle = (productLabel: string, label: string, key: (typeof PROJECT_SERVICE_KEYS)[number]) => (
    <SettingsSwitchField ariaLabel={`${t(productLabel)} · ${t(label)}`} checked={form[key]} label={t(label)} onChange={(value) => update(key, value)} />
  );
  const save = async () => {
    if (PRODUCT_TYPES.some((type) => !form[`default_project_${type}_code_enabled`] && !form[`default_project_${type}_purchase_enabled`])) {
      Toast.error(t("每种商品至少默认启用接码或购买中的一项"));
      return;
    }
    setSaving(true);
    try {
      await onBulkSave([...PROJECT_PRICE_KEYS, ...PROJECT_SERVICE_KEYS].map((key) => ({ key, value: String(form[key]) })));
    } finally {
      setSaving(false);
    }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<PackageCheck size={16} />} title={t("项目商品默认配置")} description={t("新建或审批项目时自动带入，可在项目管理中继续调整")} />}>
    <div className="mt-4 text-sm font-medium text-[var(--semi-color-text-0)]">{t("微软邮箱")}</div>
    <SettingsFormGrid className="mt-3">
      {field("接码价", "default_project_microsoft_code_price")}
      {field("接码结算价", "default_project_microsoft_code_supplier_price")}
      {field("购买价", "default_project_microsoft_purchase_price")}
      {field("购买结算价", "default_project_microsoft_purchase_supplier_price")}
      {toggle("微软邮箱", "默认启用接码", "default_project_microsoft_code_enabled")}
      {toggle("微软邮箱", "默认启用购买", "default_project_microsoft_purchase_enabled")}
    </SettingsFormGrid>
    <div className="mt-5 text-sm font-medium text-[var(--semi-color-text-0)]">{t("域名邮箱")}</div>
    <SettingsFormGrid className="mt-3">
      {field("接码价", "default_project_domain_code_price")}
      {field("接码结算价", "default_project_domain_code_supplier_price")}
      {field("购买价", "default_project_domain_purchase_price")}
      {field("购买结算价", "default_project_domain_purchase_supplier_price")}
      {toggle("域名邮箱", "默认启用接码", "default_project_domain_code_enabled")}
      {toggle("域名邮箱", "默认启用购买", "default_project_domain_purchase_enabled")}
    </SettingsFormGrid>
    <div className="mt-5 text-sm font-medium text-[var(--semi-color-text-0)]">Gmail</div>
    <SettingsFormGrid className="mt-3">
      {field("接码价", "default_project_gmail_code_price")}
      {field("接码结算价", "default_project_gmail_code_supplier_price")}
      {field("购买价", "default_project_gmail_purchase_price")}
      {field("购买结算价", "default_project_gmail_purchase_supplier_price")}
      {toggle("Gmail", "默认启用接码", "default_project_gmail_code_enabled")}
      {toggle("Gmail", "默认启用购买", "default_project_gmail_purchase_enabled")}
    </SettingsFormGrid>
    <div className="mt-5 text-sm font-medium text-[var(--semi-color-text-0)]">{t("Gmail variant")}</div>
    <SettingsFormGrid className="mt-3">
      {field("接码价", "default_project_gmail_variant_code_price")}
      {field("接码结算价", "default_project_gmail_variant_code_supplier_price")}
      {field("购买价", "default_project_gmail_variant_purchase_price")}
      {field("购买结算价", "default_project_gmail_variant_purchase_supplier_price")}
      {toggle("Gmail variant", "默认启用接码", "default_project_gmail_variant_code_enabled")}
      {toggle("Gmail variant", "默认启用购买", "default_project_gmail_variant_purchase_enabled")}
    </SettingsFormGrid>
    <div className="mt-5 text-sm font-medium text-[var(--semi-color-text-0)]">iCloud</div>
    <SettingsFormGrid className="mt-3">
      {field("接码价", "default_project_icloud_code_price")}
      {field("接码结算价", "default_project_icloud_code_supplier_price")}
      {field("购买价", "default_project_icloud_purchase_price")}
      {field("购买结算价", "default_project_icloud_purchase_supplier_price")}
      {toggle("iCloud", "默认启用接码", "default_project_icloud_code_enabled")}
      {toggle("iCloud", "默认启用购买", "default_project_icloud_purchase_enabled")}
    </SettingsFormGrid>
    <Button className="mt-5" icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary">{t("保存设置")}</Button>
  </SettingsSection>;
}
