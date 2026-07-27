import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Monitor, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { invalidNumericKeys, selectOptions, serializeOptions } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsInvalidValuesNotice, SettingsNumberField, SettingsSection } from "./settings-layout";
import { ADMIN_MONITOR_KEYS } from "./system-operations-keys";

export default function AdminMonitorSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<Record<string, unknown>>(() => selectOptions(options, ADMIN_MONITOR_KEYS));
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown): number | undefined => {
    if (value === undefined || value === null || String(value).trim() === "") return undefined;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  };
  const invalidKeys = invalidNumericKeys(form, ADMIN_MONITOR_KEYS);
  const field = (label: string, key: string, min = 1, max = 100) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={min} max={max} />;
  const save = async () => {
    setSaving(true);
    try { await onBulkSave(serializeOptions(ADMIN_MONITOR_KEYS, form, ADMIN_MONITOR_KEYS)); }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Monitor size={16} />} title={t("管理面板与系统监控")} description={t("配置排行榜、看板缓存、系统缓存和慢请求阈值")} />}>
    <SettingsFormGrid className="mt-4">
      {field("排行榜显示条数", "admin_ranking_limit")}
      {field("消息搜索关键词最大字符数", "admin_message_max_search", 1, 120)}
      {field("控制台数据缓存有效期（小时）", "dashboard_cache_ttl_hours", 1, 8760)}
      {field("排行榜缓存有效期（分钟）", "leaderboard_cache_ttl_minutes", 1, 1440)}
      {field("排行榜刷新间隔（分钟）", "ranking_refresh_interval_minutes", 1, 1440)}
      {field("资源筛选缓存有效期（秒）", "resource_facets_cache_ttl_seconds", 1, 3600)}
      {field("管理端资源统计缓存有效期（秒）", "admin_resource_facets_cache_ttl_seconds", 1, 3600)}
      {field("进程内缓存最大条目", "ttl_cache_max_entries", 1, 1000000)}
      {field("慢请求告警阈值（毫秒）", "slow_request_threshold_ms", 0, 600000)}
      {field("慢 SQL 告警阈值（毫秒）", "slow_sql_threshold_ms", 0, 600000)}
    </SettingsFormGrid>
    <SettingsInvalidValuesNotice keys={invalidKeys} message={t("检测到无效数字配置，请修正后再保存")} />
    <Button icon={<Save size={14} />} disabled={invalidKeys.length > 0} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
