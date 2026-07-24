import { useState } from "react";
import { Button } from "@douyinfe/semi-ui";
import { Network, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { invalidNumericKeys, selectOptions, serializeOptions } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsInvalidValuesNotice, SettingsNumberField, SettingsSection } from "./settings-layout";
import { PROXY_NETWORK_KEYS } from "./email-service-keys";

export default function ProxySection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<Record<string, unknown>>(() => selectOptions(options, PROXY_NETWORK_KEYS));
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown): number | undefined => {
    if (value === undefined || value === null || String(value).trim() === "") return undefined;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  };
  const invalidKeys = invalidNumericKeys(form, PROXY_NETWORK_KEYS);
  const field = (label: string, key: string) => <SettingsNumberField label={t(label)} value={number(form[key])} onChange={(value) => update(key, value)} min={1} />;
  const save = async () => {
    setSaving(true);
    try {
      await onBulkSave(serializeOptions(PROXY_NETWORK_KEYS, form, PROXY_NETWORK_KEYS));
    }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Network size={16} />} title={t("代理与网络")} description={t("配置代理检测、资源绑定、连接池和 TLS 握手参数")} />}>
    <SettingsFormGrid className="mt-4">
      {field("代理检测调度间隔（秒）", "proxy_check_interval_seconds")}
      {field("代理失败阈值", "proxy_failure_threshold")}
      {field("代理检测请求超时（秒）", "proxy_check_timeout_seconds")}
      {field("资源代理绑定有效期（天）", "resource_binding_ttl_days")}
      {field("代理获取尝试硬上限", "max_proxy_attempts")}
      {field("待检测代理查询上限", "pending_proxy_check_limit")}
      {field("代理空闲连接超时（秒）", "proxy_idle_conn_timeout_seconds")}
      {field("代理 TLS 握手超时（秒）", "proxy_tls_handshake_timeout_seconds")}
    </SettingsFormGrid>
    <SettingsInvalidValuesNotice keys={invalidKeys} message={t("检测到无效数字配置，请修正后再保存")} />
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
