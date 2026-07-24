import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Save, ShoppingCart } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const D = { order_lifecycle_scanner_interval_seconds: 30, checkout_batch_concurrency: 1024, checkout_batch_max_waiting: 1024, checkout_batch_unit_size: 20 };

export default function OrderSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D) as Record<string, unknown>);
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const save = async () => {
    setSaving(true);
    try { await onBulkSave(Object.entries(form).map(([key, value]) => ({ key, value: String(value) }))); }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<ShoppingCart size={16} />} title={t("订单与交易")} description={t("配置订单生命周期扫描、结账并发和排队批次")} />}>
    <SettingsFormGrid className="mt-4">
      <SettingsNumberField label={t("订单生命周期扫描间隔（秒）")} value={number(form.order_lifecycle_scanner_interval_seconds)} onChange={(value) => update("order_lifecycle_scanner_interval_seconds", value)} min={0} />
      <SettingsNumberField label={t("结账并发数")} value={number(form.checkout_batch_concurrency)} onChange={(value) => update("checkout_batch_concurrency", value)} min={0} />
      <SettingsNumberField label={t("结账排队容量")} value={number(form.checkout_batch_max_waiting)} onChange={(value) => update("checkout_batch_max_waiting", value)} min={0} />
      <SettingsNumberField label={t("结账批次单位")} value={number(form.checkout_batch_unit_size)} onChange={(value) => update("checkout_batch_unit_size", value)} min={0} />
    </SettingsFormGrid>
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
