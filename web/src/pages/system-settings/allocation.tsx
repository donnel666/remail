import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Gauge, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { invalidNumericKeys, selectOptions, serializeOptions } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsInvalidValuesNotice, SettingsNumberField, SettingsSection } from "./settings-layout";
import { ALLOCATION_KEYS } from "./email-service-keys";

export default function AllocationSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<Record<string, unknown>>(() => selectOptions(options, ALLOCATION_KEYS));
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown): number | undefined => {
    if (value === undefined || value === null || String(value).trim() === "") return undefined;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  };
  const invalidKeys = invalidNumericKeys(form, ALLOCATION_KEYS);
  const field = (label: string, key: string) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={1} />;
  const save = async () => {
    setSaving(true);
    try {
      await onBulkSave(serializeOptions(ALLOCATION_KEYS, form, ALLOCATION_KEYS));
    }
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
    <SettingsInvalidValuesNotice keys={invalidKeys} message={t("检测到无效数字配置，请修正后再保存")} />
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
