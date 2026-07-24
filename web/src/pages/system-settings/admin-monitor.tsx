import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Monitor, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/settings-api-mock";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const D = { admin_resource_list_default_limit: 20, admin_resource_list_max_limit: 100, admin_log_default_limit: 20, admin_log_max_limit: 100, admin_task_default_limit: 20, admin_task_max_limit: 100, admin_ranking_limit: 10, admin_message_default_limit: 20, admin_message_max_limit: 100, admin_message_max_search: 120, dashboard_cache_ttl_hours: 24, leaderboard_cache_ttl_minutes: 15, ranking_refresh_interval_minutes: 5, api_key_meta_ttl_seconds: 30, api_key_cache_flush_interval_seconds: 5, resource_facets_cache_ttl_seconds: 10, ttl_cache_max_entries: 4096, slow_request_threshold_ms: 1000, slow_sql_threshold_ms: 200 };

export default function AdminMonitorSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D) as Record<string, unknown>);
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const field = (label: string, key: string) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={0} />;
  const save = async () => {
    setSaving(true);
    try { await onBulkSave(Object.entries(form).map(([key, value]) => ({ key, value: String(value) }))); }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Monitor size={16} />} title={t("管理面板与系统监控")} description={t("配置管理列表分页、看板缓存、API Key 缓存和慢请求阈值")} />}>
    <SettingsFormGrid className="mt-4">
      {field("资源列表默认每页条数", "admin_resource_list_default_limit")}
      {field("资源列表最大每页条数", "admin_resource_list_max_limit")}
      {field("操作日志默认每页条数", "admin_log_default_limit")}
      {field("操作日志最大每页条数", "admin_log_max_limit")}
      {field("任务列表默认每页条数", "admin_task_default_limit")}
      {field("任务列表最大每页条数", "admin_task_max_limit")}
      {field("排行榜显示条数", "admin_ranking_limit")}
      {field("消息列表默认每页条数", "admin_message_default_limit")}
      {field("消息列表最大每页条数", "admin_message_max_limit")}
      {field("消息搜索最大条数", "admin_message_max_search")}
      {field("控制台数据缓存有效期（小时）", "dashboard_cache_ttl_hours")}
      {field("排行榜缓存有效期（分钟）", "leaderboard_cache_ttl_minutes")}
      {field("排行榜刷新间隔（分钟）", "ranking_refresh_interval_minutes")}
      {field("API Key 元数据缓存有效期（秒）", "api_key_meta_ttl_seconds")}
      {field("API Key 缓存刷新间隔（秒）", "api_key_cache_flush_interval_seconds")}
      {field("资源筛选缓存有效期（秒）", "resource_facets_cache_ttl_seconds")}
      {field("进程内缓存最大条目", "ttl_cache_max_entries")}
      {field("慢请求告警阈值（毫秒）", "slow_request_threshold_ms")}
      {field("慢 SQL 告警阈值（毫秒）", "slow_sql_threshold_ms")}
    </SettingsFormGrid>
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save()} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
