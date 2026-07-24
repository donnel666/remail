import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Gauge, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const D: Record<string, unknown> = { candidate_window_size: 4, global_candidate_window: 8, bucket_probe_count: 4, alias_generation_window: 32, candidate_retry_count: 5, dot_alias_capacity_per_resource: 10, inventory_refresh_interval_minutes: 10, inventory_cache_activity_ttl_minutes: 20, inventory_cache_hard_ttl_hours: 24 };

export default function AllocationSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D as any) as Record<string, unknown>);
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const field = (label: string, key: string) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={1} />;
  const save = async () => {
    setSaving(true);
    try { await onBulkSave(Object.entries(form).map(([key, value]) => ({ key, value: String(value) }))); }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Gauge size={16} />} title={t("分配引擎")} description={t("配置候选选择、别名容量和库存缓存策略")} />}>
    <SettingsFormGrid className="mt-4">
      {field("单轮候选窗口", "candidate_window_size")}
      {field("全局候选窗口", "global_candidate_window")}
      {field("每轮探测桶数量", "bucket_probe_count")}
      {field("别名生成窗口", "alias_generation_window")}
      {field("候选获取重试次数", "candidate_retry_count")}
      {field("点别名生成位置窗口", "dot_alias_capacity_per_resource")}
      {field("库存刷新间隔（分钟）", "inventory_refresh_interval_minutes")}
      {field("库存缓存活跃有效期（分钟）", "inventory_cache_activity_ttl_minutes")}
      {field("库存缓存硬过期（小时）", "inventory_cache_hard_ttl_hours")}
    </SettingsFormGrid>
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
