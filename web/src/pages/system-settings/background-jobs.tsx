import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Cpu, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { invalidNumericKeys, selectOptions, serializeOptions } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsInvalidValuesNotice, SettingsNumberField, SettingsSection } from "./settings-layout";
import { BACKGROUND_JOB_KEYS } from "./system-operations-keys";

export default function BackgroundJobSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<Record<string, unknown>>(() => selectOptions(options, BACKGROUND_JOB_KEYS));
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown): number | undefined => {
    if (value === undefined || value === null || String(value).trim() === "") return undefined;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  };
  const invalidKeys = invalidNumericKeys(form, BACKGROUND_JOB_KEYS);
  const field = (label: string, key: string, min = 1, max = 8096) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={min} max={max} />;
  const save = async () => {
    setSaving(true);
    try { await onBulkSave(serializeOptions(BACKGROUND_JOB_KEYS, form, BACKGROUND_JOB_KEYS)); }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Cpu size={16} />} title={t("后台任务调度")} description={t("配置自适应并发、Asynq Worker、队列调度权重和重试；Worker 并发、队列权重与停机超时重启后生效")} />}>
    <SettingsFormGrid className="mt-4">
      {field("负载过载阈值（%）", "background_load_overload_percent", 11, 100)}
      {field("最小并发数", "background_worker_minimum")}
      {field("初始并发数", "background_worker_initial")}
      {field("并发增长步长", "background_worker_increase_step")}
      {field("恢复确认采样次数", "background_recovery_samples", 1, 100)}
      {field("指标失败容忍次数", "background_metric_failure_limit", 1, 100)}
      {field("任务最大重试次数", "background_task_max_retry", 0, 20)}
      {field("后台容量不足重试最小延迟（秒）", "background_retry_delay_minimum_seconds", 1, 3600)}
      {field("后台容量不足重试抖动（秒）", "background_retry_delay_jitter_seconds", 0, 3600)}
      {field("通用队列 Worker 并发数", "asynq_worker_concurrency")}
      {field("实时队列 Worker 并发数", "asynq_realtime_worker_concurrency")}
      {field("后台队列 Worker 并发数", "asynq_background_worker_concurrency")}
      {field("实时收件队列权重（mailfetch）", "asynq_queue_mailfetch_weight", 1, 10000)}
      {field("支付对账队列权重（payment_reconcile）", "asynq_queue_payment_reconcile_weight", 1, 10000)}
      {field("邮件发送队列权重（mailtransport）", "asynq_queue_mailtransport_weight", 1, 10000)}
      {field("通用任务队列权重（default）", "asynq_queue_default_weight", 1, 10000)}
      {field("微软验证队列权重（background_validation）", "asynq_queue_background_validation_weight", 1, 10000)}
      {field("域名验证队列权重（background_domain_validation）", "asynq_queue_background_domain_validation_weight", 1, 10000)}
      {field("别名维护队列权重（background_alias）", "asynq_queue_background_alias_weight", 1, 10000)}
      {field("Token 刷新队列权重（background_token_refresh）", "asynq_queue_background_token_refresh_weight", 1, 10000)}
      {field("资源批处理队列权重（resource）", "asynq_queue_resource_weight", 1, 10000)}
      {field("项目历史队列权重（background_project_history）", "asynq_queue_background_project_history_weight", 1, 10000)}
      {field("库存刷新队列权重（background_inventory）", "asynq_queue_background_inventory_weight", 1, 10000)}
      {field("停机超时（秒）", "asynq_shutdown_timeout_seconds", 1, 300)}
      {field("验证调度最大下发数", "validation_dispatch_maximum", 1, 10000)}
      {field("入站 SMTP 最大并发连接数", "default_inbound_smtp_max_connections", 1, 10000)}
    </SettingsFormGrid>
    <SettingsInvalidValuesNotice keys={invalidKeys} message={t("检测到无效数字配置，请修正后再保存")} />
    <Button icon={<Save size={14} />} disabled={invalidKeys.length > 0} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
