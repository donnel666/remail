import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { DatabaseZap, Save, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/settings-api-mock";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const D = { admin_resource_bulk_max_ids: 1000, admin_domain_bulk_max_ids: 1000, admin_domain_bulk_max_filter: 10000, resource_validation_max_ids: 10000, validation_batch_page_size: 1000, validation_insert_chunk_size: 1000, bulk_user_chunk_size: 5000, card_bulk_chunk_size: 5000, retention_batch_size: 5000, retention_batch_sleep_ms: 200, retention_daily_run_hour: 4, idempotency_key_retain_days: 30, mailmatch_ms_retain_days: 3, mailmatch_domain_retain_days: 30, daily_usage_retain_days: 14, outbound_mail_retain_days: 30, inbound_mail_retain_days: 30, system_log_retain_days: 30 };
const BATCH_KEYS = ["admin_resource_bulk_max_ids", "admin_domain_bulk_max_ids", "admin_domain_bulk_max_filter", "resource_validation_max_ids", "validation_batch_page_size", "validation_insert_chunk_size", "bulk_user_chunk_size", "card_bulk_chunk_size", "retention_batch_size", "retention_batch_sleep_ms", "retention_daily_run_hour"];
const RETENTION_KEYS = ["idempotency_key_retain_days", "mailmatch_ms_retain_days", "mailmatch_domain_retain_days", "daily_usage_retain_days", "outbound_mail_retain_days", "inbound_mail_retain_days", "system_log_retain_days"];

export default function BatchDataSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D) as Record<string, unknown>);
  const [savingCard, setSavingCard] = useState<string | null>(null);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const field = (label: string, key: string) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={0} />;
  const save = async (card: string, keys: string[]) => {
    setSavingCard(card);
    try { await onBulkSave(keys.map((key) => ({ key, value: String(form[key] ?? "") }))); }
    finally { setSavingCard(null); }
  };

  return <div className="space-y-6">
    <SettingsSection title={<SettingsCardHeader icon={<DatabaseZap size={16} />} title={t("批量操作与数据清理")} description={t("配置批量请求规模、验证分块和每日清理批次")} />}>
      <SettingsFormGrid className="mt-4">
        {field("资源批量操作最大 ID 数", "admin_resource_bulk_max_ids")}
        {field("域名批量操作最大 ID 数", "admin_domain_bulk_max_ids")}
        {field("域名批量筛选最大命中数", "admin_domain_bulk_max_filter")}
        {field("资源验证请求最大 ID 数", "resource_validation_max_ids")}
        {field("验证批次分页大小", "validation_batch_page_size")}
        {field("验证插入分块大小", "validation_insert_chunk_size")}
        {field("用户批量操作分块大小", "bulk_user_chunk_size")}
        {field("卡券批量操作分块大小", "card_bulk_chunk_size")}
        {field("数据清理批次大小", "retention_batch_size")}
        {field("数据清理批次间隔（毫秒）", "retention_batch_sleep_ms")}
        {field("数据清理执行时间（小时）", "retention_daily_run_hour")}
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "batch"} onClick={() => void save("batch", BATCH_KEYS)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader icon={<Trash2 size={16} />} title={t("数据保留策略")} description={t("配置各类业务记录和系统日志的保留天数")} />}>
      <SettingsFormGrid className="mt-4">
        {field("幂等键保留天数", "idempotency_key_retain_days")}
        {field("微软邮箱接码消息保留天数", "mailmatch_ms_retain_days")}
        {field("域名邮箱接码消息保留天数", "mailmatch_domain_retain_days")}
        {field("每日用量记录保留天数", "daily_usage_retain_days")}
        {field("外发邮件记录保留天数", "outbound_mail_retain_days")}
        {field("入站邮件记录保留天数", "inbound_mail_retain_days")}
        {field("系统日志保留天数", "system_log_retain_days")}
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "retention"} onClick={() => void save("retention", RETENTION_KEYS)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>
  </div>;
}
