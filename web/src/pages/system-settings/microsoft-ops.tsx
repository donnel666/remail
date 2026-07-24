import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Save, Wrench } from "lucide-react";
import { useTranslation } from "react-i18next";

import { invalidNumericKeys, selectOptions, serializeOptions } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsInvalidValuesNotice, SettingsNumberField, SettingsSection } from "./settings-layout";
import { MICROSOFT_OPS_KEYS } from "./email-service-keys";

export default function MicrosoftOpsSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<Record<string, unknown>>(() => selectOptions(options, MICROSOFT_OPS_KEYS));
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown): number | undefined => {
    if (value === undefined || value === null || String(value).trim() === "") return undefined;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  };
  const invalidKeys = invalidNumericKeys(form, MICROSOFT_OPS_KEYS);
  const field = (label: string, key: string) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={1} />;
  const save = async () => {
    setSaving(true);
    try {
      await onBulkSave(serializeOptions(MICROSOFT_OPS_KEYS, form, MICROSOFT_OPS_KEYS));
    }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Wrench size={16} />} title={t("微软邮箱运维")} description={t("配置别名补充、Token 刷新、密码恢复和协议客户端参数")} />}>
    <SettingsFormGrid className="mt-4">
      {field("每周别名创建上限", "microsoft_alias_weekly_limit")}
      {field("每年别名创建上限", "microsoft_alias_yearly_limit")}
      {field("别名补充检查间隔（小时）", "microsoft_alias_ensure_interval_hours")}
      {field("别名对账宽限期（小时）", "microsoft_alias_reconciliation_grace_hours")}
      {field("临时错误退避基准（分钟）", "microsoft_alias_transient_backoff_base_minutes")}
      {field("临时错误退避上限（小时）", "microsoft_alias_transient_backoff_max_hours")}
      {field("别名跳过确认次数", "microsoft_alias_negative_confirm_required")}
      {field("Token 刷新最大重试次数", "token_refresh_max_attempts")}
      {field("Token 刷新扫描上限", "token_refresh_scan_limit")}
      {field("Token 刷新提前量（天）", "token_refresh_lookahead_days")}
      <SettingsNumberField label={t("Token 刷新触发时间（小时）")} value={number(form.token_refresh_hour)} onChange={(value) => update("token_refresh_hour", value)} min={0} max={23} />
      {field("恢复码租约（分钟）", "recovery_code_lease_minutes")}
      {field("密码恢复验证码等待（秒）", "password_recovery_code_wait_seconds")}
      {field("Token 轮询最小预算（秒）", "msacl_token_poll_timeout_seconds")}
      {field("Token 轮询本地最小间隔（秒）", "msacl_token_poll_interval_seconds")}
      {field("IMAP 操作超时（秒）", "imap_operation_timeout_seconds")}
      {field("IMAP 全历史超时（分钟）", "imap_full_history_timeout_minutes")}
      {field("代理握手超时（秒）", "proxy_handshake_timeout_seconds")}
      {field("Graph 单页拉取条数", "graph_message_page_top")}
      {field("邮件流批处理大小", "mail_stream_batch_size")}
      {field("邮件拉取客户端超时（秒）", "mail_fetch_client_timeout_seconds")}
      {field("IMAP 拨号超时（秒）", "imap_dial_timeout_seconds")}
      {field("IMAP 保活间隔（秒）", "imap_keepalive_seconds")}
      {field("OAuth 与微软浏览器请求超时（秒）", "oauth_validation_timeout_seconds")}
    </SettingsFormGrid>
    <SettingsInvalidValuesNotice keys={invalidKeys} message={t("检测到无效数字配置，请修正后再保存")} />
    <Button icon={<Save size={14} />} disabled={invalidKeys.length > 0} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
