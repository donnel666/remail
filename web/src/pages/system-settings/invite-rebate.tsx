import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Save, Tag } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const D: Record<string, unknown> = { first_order_rebate_ratio: 0.8, single_rebate_cap: 0, cumulative_rebate_cap: 0, rebate_expiry_days: 90 };

export default function InviteRebateSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D as any) as Record<string, unknown>);
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const save = async () => {
    setSaving(true);
    try { await onBulkSave(Object.entries(form).map(([key, value]) => ({ key, value: String(value) }))); }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Tag size={16} />} title={t("邀请返利")} description={t("配置首单返利比例、金额上限和有效期")} />}>
    <SettingsFormGrid className="mt-4">
      <SettingsNumberField label={t("首单返利比例（0.8 = 80%）")} value={number(form.first_order_rebate_ratio)} onChange={(value) => update("first_order_rebate_ratio", value)} min={0} />
      <SettingsNumberField label={t("单笔返利金额上限（0 = 不限制）")} value={number(form.single_rebate_cap)} onChange={(value) => update("single_rebate_cap", value)} min={0} />
      <SettingsNumberField label={t("累计返利金额上限（0 = 不限制）")} value={number(form.cumulative_rebate_cap)} onChange={(value) => update("cumulative_rebate_cap", value)} min={0} />
      <SettingsNumberField label={t("返利有效期（天）")} value={number(form.rebate_expiry_days)} onChange={(value) => update("rebate_expiry_days", value)} min={0} />
    </SettingsFormGrid>
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save()} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
