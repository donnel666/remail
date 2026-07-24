import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Save, Send } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const D = { smtp_outbound_payload_ttl_minutes: 5, smtp_task_retry_count: 3, outbound_mail_timeout_minutes: 3, inbound_mail_timeout_minutes: 2, auxiliary_domain_refresh_interval_seconds: 60, max_inbound_header_runes: 500, max_inbound_preview_runes: 1000, max_inbound_body_bytes: 1048576, max_inbound_body_runes: 200000, max_inbound_mime_depth: 12, mail_dispatcher_interval_seconds: 15, alias_dispatcher_interval_seconds: 2, token_refresh_dispatcher_interval_seconds: 2, legacy_alias_retry_delay_seconds: 30 };
const BYTES_PER_MB = 1024 * 1024;

export default function MailDeliverySection({ options, onBulkSave }: SectionProps) {
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

  return <SettingsSection title={<SettingsCardHeader icon={<Send size={16} />} title={t("邮件投递")} description={t("配置入站解析、外发超时和邮件调度间隔")} />}>
    <SettingsFormGrid className="mt-4">
      {field("SMTP 邮件临时内容 TTL（分钟）", "smtp_outbound_payload_ttl_minutes")}
      <SettingsNumberField label={t("SMTP 任务内部重试次数")} value={number(form.smtp_task_retry_count)} onChange={(value) => update("smtp_task_retry_count", value)} min={0} max={20} />
      {field("外发邮件任务超时（分钟）", "outbound_mail_timeout_minutes")}
      {field("入站邮件处理超时（分钟）", "inbound_mail_timeout_minutes")}
      {field("辅助域名刷新间隔（秒）", "auxiliary_domain_refresh_interval_seconds")}
      {field("邮件头部最大字符数", "max_inbound_header_runes")}
      {field("邮件预览最大字符数", "max_inbound_preview_runes")}
      <SettingsNumberField label={t("邮件正文最大体积（MB）")} value={number(form.max_inbound_body_bytes) / BYTES_PER_MB} onChange={(value) => update("max_inbound_body_bytes", Math.round(value * BYTES_PER_MB))} min={1} />
      {field("邮件正文最大字符数", "max_inbound_body_runes")}
      {field("MIME 嵌套最大深度", "max_inbound_mime_depth")}
      {field("入站邮件补偿调度间隔（秒）", "mail_dispatcher_interval_seconds")}
      {field("别名调度间隔（秒）", "alias_dispatcher_interval_seconds")}
      {field("Token 刷新调度间隔（秒）", "token_refresh_dispatcher_interval_seconds")}
      {field("旧格式别名重试延迟（秒）", "legacy_alias_retry_delay_seconds")}
    </SettingsFormGrid>
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
