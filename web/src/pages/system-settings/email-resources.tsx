import { useState } from "react";
import { Button, TagInput } from "@douyinfe/semi-ui";
import { Database, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { invalidNumericKeys, selectOptions, serializeOptions } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { FormItem, FormLabel, SettingsCardHeader, SettingsFormGrid, SettingsInvalidValuesNotice, SettingsNumberField, SettingsSection } from "./settings-layout";
import { EMAIL_RESOURCE_KEYS } from "./email-service-keys";
const BYTES_PER_MB = 1024 * 1024;
const NUMERIC_KEYS = EMAIL_RESOURCE_KEYS.filter((key) => key !== "microsoft_domain_whitelist");

export default function EmailResourceSection({ options, onBulkSave }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<Record<string, unknown>>(() => selectOptions(options, EMAIL_RESOURCE_KEYS));
  const [saving, setSaving] = useState(false);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown): number | undefined => {
    if (value === undefined || value === null || String(value).trim() === "") return undefined;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  };
  const domains = typeof form.microsoft_domain_whitelist === "string"
    ? form.microsoft_domain_whitelist.split(/[\s,，]+/).map((domain) => domain.trim()).filter(Boolean)
    : [];
  const invalidKeys = invalidNumericKeys(form, NUMERIC_KEYS);
  const save = async () => {
    setSaving(true);
    try {
      await onBulkSave(serializeOptions(EMAIL_RESOURCE_KEYS, form, NUMERIC_KEYS));
    }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Database size={16} />} title={t("邮箱资源与域名")} description={t("配置微软邮箱域名、默认配额、资源验证和项目输入限制")} />}>
    <SettingsFormGrid className="mt-4">
      <FormItem spanFull>
        <FormLabel>{t("微软邮箱域名白名单")}</FormLabel>
        <TagInput aria-label={t("微软邮箱域名白名单")} value={domains} separator={[",", "，", " ", "\n"]} allowDuplicates={false} addOnBlur showClear placeholder={t("输入邮箱域名后回车")} onChange={(values) => update("microsoft_domain_whitelist", values.map((value) => value.trim()).filter(Boolean).join(","))} style={{ width: "100%" }} />
        <p className="text-xs text-[var(--semi-color-text-2)]">{t("每个允许导入的微软邮箱域名单独显示；留空使用系统内置白名单")}</p>
      </FormItem>
      <SettingsNumberField label={t("子地址默认日配额")} value={number(form.default_plus_daily_limit)} onChange={(value) => update("default_plus_daily_limit", value)} min={1} />
      <SettingsNumberField label={t("邮箱默认日配额")} value={number(form.default_mailbox_daily_limit)} onChange={(value) => update("default_mailbox_daily_limit", value)} min={1} />
      <SettingsNumberField label={t("验证最大连续失败次数")} value={number(form.resource_validation_max_failures)} onChange={(value) => update("resource_validation_max_failures", value)} min={1} />
      <SettingsNumberField label={t("资源导入文件最大体积（MB）")} value={number(form.resource_import_max_bytes) === undefined ? undefined : number(form.resource_import_max_bytes)! / BYTES_PER_MB} onChange={(value) => update("resource_import_max_bytes", Math.round(value * BYTES_PER_MB))} min={1} />
      <SettingsNumberField label={t("项目 Logo 最大体积（MB）")} value={number(form.max_project_logo_bytes) === undefined ? undefined : number(form.max_project_logo_bytes)! / BYTES_PER_MB} onChange={(value) => update("max_project_logo_bytes", Math.round(value * BYTES_PER_MB))} min={1} />
      <SettingsNumberField label={t("项目名称最大长度")} value={number(form.project_name_max)} onChange={(value) => update("project_name_max", value)} min={1} />
      <SettingsNumberField label={t("项目描述最大长度")} value={number(form.project_description_max)} onChange={(value) => update("project_description_max", value)} min={1} />
      <SettingsNumberField label={t("目标平台名最大长度")} value={number(form.project_target_platform_max)} onChange={(value) => update("project_target_platform_max", value)} min={1} />
    </SettingsFormGrid>
    <SettingsInvalidValuesNotice keys={invalidKeys} message={t("检测到无效数字配置，请修正后再保存")} />
    <Button icon={<Save size={14} />} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
