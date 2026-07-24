import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { MailSearch, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection, SettingsTextField } from "./settings-layout";

const D = { fetch_lookback_window_days: 90, read_window_skew_minutes: 2, code_read_limit: 1, purchase_read_limit: 30, message_scan_limit: 40, projection_replay_limit: 100, pickup_fetch_reserve_ttl_minutes: 2, pickup_fetch_lease_ttl_minutes: 2, pickup_message_cache_ttl_seconds: 10, pickup_message_cache_limit: 30, pickup_fetch_heartbeat_seconds: 30, mailmatch_fetch_timeout_minutes: 20, pickup_request_fetch_timeout_minutes: 2, project_history_timeout_minutes: 20, fetch_dispatcher_interval_seconds: 15, project_history_concurrency: 4, project_history_dispatch_limit: 4, verification_code_pattern: "(^|[^\\d])(\\d{6,8})([^\\d]|$)" };

export default function MailmatchSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D) as Record<string, unknown>);
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const field = (label: string, key: string) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={1} />;
  const save = async () => {
    setSaving(true);
    try { await onBulkSave(Object.entries(form).map(([key, value]) => ({ key, value: String(value) }))); }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<MailSearch size={16} />} title={t("接码服务")} description={t("配置邮件拉取、匹配、接码会话缓存和调度参数")} />}>
    <SettingsFormGrid className="mt-4">
      {field("邮件回溯窗口（天）", "fetch_lookback_window_days")}
      {field("时间偏差容忍（分钟）", "read_window_skew_minutes")}
      {field("验证码读取上限", "code_read_limit")}
      {field("购买邮件读取上限", "purchase_read_limit")}
      {field("邮件扫描上限", "message_scan_limit")}
      {field("投影重放上限", "projection_replay_limit")}
      {field("接码预留有效期（分钟）", "pickup_fetch_reserve_ttl_minutes")}
      {field("接码租约有效期（分钟）", "pickup_fetch_lease_ttl_minutes")}
      {field("消息缓存有效期（秒）", "pickup_message_cache_ttl_seconds")}
      {field("消息缓存条数", "pickup_message_cache_limit")}
      {field("心跳间隔（秒）", "pickup_fetch_heartbeat_seconds")}
      {field("拉取任务超时（分钟）", "mailmatch_fetch_timeout_minutes")}
      {field("接码请求超时（分钟）", "pickup_request_fetch_timeout_minutes")}
      {field("项目历史超时（分钟）", "project_history_timeout_minutes")}
      {field("拉取调度间隔（秒）", "fetch_dispatcher_interval_seconds")}
      {field("项目历史并发数", "project_history_concurrency")}
      {field("项目历史每轮上限", "project_history_dispatch_limit")}
      <SettingsTextField label={t("验证码识别正则")} value={String(form.verification_code_pattern)} onChange={(value) => update("verification_code_pattern", value)} />
    </SettingsFormGrid>
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
