import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Save, Tag } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";
import { RECHARGE_REBATE_KEYS } from "./users-rebates-keys";

const D: Record<string, unknown> = { first_order_rebate_ratio: 0.8, single_rebate_cap: 0, cumulative_rebate_cap: 0, rebate_expiry_days: 90 };

export default function RechargeRebateSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D as any) as Record<string, unknown>);
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const save = async () => {
    setSaving(true);
    try { await onBulkSave(RECHARGE_REBATE_KEYS.map((key) => ({ key, value: String(form[key]) }))); }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Tag size={16} />} title={t("充值返利")} description={t("配置被邀请用户首次充值后的返利比例、金额上限和有效期")} />}>
    <SettingsFormGrid className="mt-4">
      <SettingsNumberField label={t("首次充值返利比例（0.8 = 80%）")} value={number(form.first_order_rebate_ratio)} onChange={(value) => update("first_order_rebate_ratio", value)} min={0} max={1} precision={2} step={0.01} />
      <SettingsNumberField label={t("单笔充值返利上限（0 = 不限制）")} value={number(form.single_rebate_cap)} onChange={(value) => update("single_rebate_cap", value)} min={0} precision={2} />
      <SettingsNumberField label={t("累计充值返利上限（0 = 不限制）")} value={number(form.cumulative_rebate_cap)} onChange={(value) => update("cumulative_rebate_cap", value)} min={0} precision={2} />
      <SettingsNumberField label={t("返利有效期（天，0 = 永不过期）")} value={number(form.rebate_expiry_days)} onChange={(value) => update("rebate_expiry_days", value)} min={0} max={36500} precision={0} />
    </SettingsFormGrid>
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
