import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { DatabaseZap, Save, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { invalidNumericKeys, selectOptions, serializeOptions } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsInvalidValuesNotice, SettingsNumberField, SettingsSection } from "./settings-layout";
import { BATCH_OPERATION_KEYS, RETENTION_KEYS } from "./system-operations-keys";

export default function BatchDataSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const keys = [...BATCH_OPERATION_KEYS, ...RETENTION_KEYS] as const;
  const [form, setForm] = useState<Record<string, unknown>>(() => selectOptions(options, keys));
  const [savingCard, setSavingCard] = useState<string | null>(null);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown): number | undefined => {
    if (value === undefined || value === null || String(value).trim() === "") return undefined;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  };
  const invalidKeys = invalidNumericKeys(form, keys);
  const batchInvalidKeys = invalidKeys.filter((key) => BATCH_OPERATION_KEYS.includes(key as typeof BATCH_OPERATION_KEYS[number]));
  const retentionInvalidKeys = invalidKeys.filter((key) => RETENTION_KEYS.includes(key as typeof RETENTION_KEYS[number]));
  const field = (label: string, key: string, min = 1, max = 10000) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={min} max={max} />;
  const save = async (card: string, selectedKeys: readonly string[]) => {
    setSavingCard(card);
    try { await onBulkSave(serializeOptions(selectedKeys, form, selectedKeys)); }
    finally { setSavingCard(null); }
  };

  return <div className="space-y-6">
    <SettingsSection title={<SettingsCardHeader icon={<DatabaseZap size={16} />} title={t("批量操作与数据清理")} description={t("配置批量请求规模、验证分块和每日清理批次")} />}>
      <SettingsFormGrid className="mt-4">
        {field("资源批量操作最大 ID 数", "admin_resource_bulk_max_ids", 1, 1000)}
        {field("域名批量操作最大 ID 数", "admin_domain_bulk_max_ids", 1, 1000)}
        {field("域名批量筛选最大命中数", "admin_domain_bulk_max_filter")}
        {field("资源验证请求最大 ID 数", "resource_validation_max_ids")}
        {field("验证批次分页大小", "validation_batch_page_size")}
        {field("验证插入分块大小", "validation_insert_chunk_size")}
        {field("用户批量操作分块大小", "bulk_user_chunk_size")}
        {field("卡券批量操作分块大小", "card_bulk_chunk_size")}
        {field("数据清理批次大小", "retention_batch_size", 1, 100000)}
        {field("数据清理批次间隔（毫秒）", "retention_batch_sleep_ms", 0, 60000)}
        {field("数据清理执行时间（小时）", "retention_daily_run_hour", 0, 23)}
      </SettingsFormGrid>
      <SettingsInvalidValuesNotice keys={batchInvalidKeys} message={t("检测到无效数字配置，请修正后再保存")} />
      <Button icon={<Save size={14} />} disabled={batchInvalidKeys.length > 0} loading={savingCard === "batch"} onClick={() => void save("batch", BATCH_OPERATION_KEYS).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader icon={<Trash2 size={16} />} title={t("数据保留策略")} description={t("配置各类业务记录和系统日志的保留天数")} />}>
      <SettingsFormGrid className="mt-4">
        {field("幂等键保留天数", "idempotency_key_retain_days", 1, 3650)}
        {field("微软邮箱接码消息保留天数", "mailmatch_ms_retain_days", 1, 3650)}
        {field("域名邮箱接码消息保留天数", "mailmatch_domain_retain_days", 1, 3650)}
        {field("Gmail 验证码保留天数", "gmail_code_retain_days", 1, 3650)}
        {field("每日用量记录保留天数", "daily_usage_retain_days", 1, 3650)}
        {field("外发邮件记录保留天数", "outbound_mail_retain_days", 1, 3650)}
        {field("入站邮件记录保留天数", "inbound_mail_retain_days", 1, 3650)}
        {field("系统日志保留天数", "system_log_retain_days", 1, 3650)}
      </SettingsFormGrid>
      <SettingsInvalidValuesNotice keys={retentionInvalidKeys} message={t("检测到无效数字配置，请修正后再保存")} />
      <Button icon={<Save size={14} />} disabled={retentionInvalidKeys.length > 0} loading={savingCard === "retention"} onClick={() => void save("retention", RETENTION_KEYS).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>
  </div>;
}
