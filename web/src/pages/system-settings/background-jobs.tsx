import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Cpu, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection } from "./settings-layout";

const D = { background_load_overload_percent: 50, background_worker_minimum: 8, background_worker_initial: 16, background_worker_increase_step: 8, background_recovery_samples: 2, background_metric_failure_limit: 3, background_task_max_retry: 5, background_retry_delay_minimum_seconds: 5, background_retry_delay_jitter_seconds: 5, asynq_worker_concurrency: 768, asynq_realtime_worker_concurrency: 256, asynq_background_worker_concurrency: 512, asynq_shutdown_timeout_seconds: 30, validation_dispatch_maximum: 128, default_inbound_smtp_max_connections: 200 };

export default function BackgroundJobSection({ options, onBulkSave }: SectionProps) {
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

  return <SettingsSection title={<SettingsCardHeader icon={<Cpu size={16} />} title={t("后台任务调度")} description={t("配置自适应并发、Asynq Worker、重试和调度吞吐")} />}>
    <SettingsFormGrid className="mt-4">
      {field("负载过载阈值（%）", "background_load_overload_percent")}
      {field("最小并发数", "background_worker_minimum")}
      {field("初始并发数", "background_worker_initial")}
      {field("并发增长步长", "background_worker_increase_step")}
      {field("恢复确认采样次数", "background_recovery_samples")}
      {field("指标失败容忍次数", "background_metric_failure_limit")}
      {field("任务最大重试次数", "background_task_max_retry")}
      {field("重试最小延迟（秒）", "background_retry_delay_minimum_seconds")}
      {field("重试抖动（秒）", "background_retry_delay_jitter_seconds")}
      {field("Worker 总并发数", "asynq_worker_concurrency")}
      {field("实时队列 Worker 并发数", "asynq_realtime_worker_concurrency")}
      {field("后台队列 Worker 并发数", "asynq_background_worker_concurrency")}
      {field("停机超时（秒）", "asynq_shutdown_timeout_seconds")}
      {field("验证调度最大下发数", "validation_dispatch_maximum")}
      {field("入站 SMTP 最大并发连接数", "default_inbound_smtp_max_connections")}
    </SettingsFormGrid>
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
