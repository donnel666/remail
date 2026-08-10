import { useState } from "react";
import { Button, TagInput } from "@douyinfe/semi-ui";
import { Database, Save } from "lucide-react";
import { useTranslation } from "react-i18next";

import { invalidNumericKeys, selectOptions, serializeOptions } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { FormItem, FormLabel, SettingsCardHeader, SettingsFormGrid, SettingsInvalidValuesNotice, SettingsNumberField, SettingsSection } from "./settings-layout";
import { EMAIL_RESOURCE_KEYS } from "./email-service-keys";
const BYTES_PER_MB = 1024 * 1024;
const NUMERIC_KEYS = EMAIL_RESOURCE_KEYS.filter((key) => key !== "microsoft_domain_whitelist" && key !== "icloud_forwarding_mailboxes" && key !== "domain_custom_tlds");

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
  const tags = (key: string) => typeof form[key] === "string"
    ? String(form[key]).split(/[\s,，;；]+/).map((value) => value.trim()).filter(Boolean)
    : [];
  const domains = tags("microsoft_domain_whitelist");
  const iCloudForwardingMailboxes = tags("icloud_forwarding_mailboxes");
  const customTLDs = tags("domain_custom_tlds");
  const invalidKeys = invalidNumericKeys(form, NUMERIC_KEYS);
  const save = async () => {
    setSaving(true);
    try {
      await onBulkSave(serializeOptions(EMAIL_RESOURCE_KEYS, form, NUMERIC_KEYS));
    }
    finally { setSaving(false); }
  };

  return <SettingsSection title={<SettingsCardHeader icon={<Database size={16} />} title={t("邮箱资源与域名")} description={t("配置微软邮箱域名、苹果转发收件邮箱、默认配额和资源验证")} />}>
    <SettingsFormGrid className="mt-4">
      <FormItem spanFull>
        <FormLabel>{t("微软邮箱域名白名单")}</FormLabel>
        <TagInput aria-label={t("微软邮箱域名白名单")} value={domains} separator={[",", "，", " ", "\n"]} allowDuplicates={false} addOnBlur showClear placeholder={t("输入邮箱域名后回车")} onChange={(values) => update("microsoft_domain_whitelist", values.map((value) => value.trim()).filter(Boolean).join(","))} style={{ width: "100%" }} />
        <p className="text-xs text-[var(--semi-color-text-2)]">{t("每个允许导入的微软邮箱域名单独显示；留空使用系统内置白名单")}</p>
      </FormItem>
      <FormItem spanFull>
        <FormLabel>{t("苹果邮箱转发收件邮箱")}</FormLabel>
        <TagInput aria-label={t("苹果邮箱转发收件邮箱")} value={iCloudForwardingMailboxes} separator={[",", "，", ";", "；", " ", "\n"]} allowDuplicates={false} addOnBlur showClear placeholder={t("输入域名邮箱后回车")} onChange={(values) => update("icloud_forwarding_mailboxes", values.map((value) => value.trim().toLowerCase()).filter(Boolean).join(","))} style={{ width: "100%" }} />
        <p className="text-xs text-[var(--semi-color-text-2)]">{t("苹果 HME 别名必须转发到列表中的任一邮箱；默认使用 icloud@aishop6.com，可配置多个邮箱。")}</p>
      </FormItem>
      <FormItem spanFull>
        <FormLabel>{t("自定义 TLD")}</FormLabel>
        <TagInput aria-label={t("自定义 TLD")} value={customTLDs} separator={[",", "，", " ", "\n"]} allowDuplicates={false} addOnBlur showClear placeholder={t("输入 TLD 后回车")} onChange={(values) => update("domain_custom_tlds", values.map((value) => value.trim()).filter(Boolean).join(","))} style={{ width: "100%" }} />
        <p className="text-xs text-[var(--semi-color-text-2)]">{t("系统默认使用 Public Suffix List；这里仅填写需要人工补充的公共后缀，例如 edu.kg")}</p>
      </FormItem>
      <SettingsNumberField label={t("每个可注册域名最多子域名数")} description={t("根域名本身不计入限额")} value={number(form.domain_max_subdomains_per_registrable_domain)} onChange={(value) => update("domain_max_subdomains_per_registrable_domain", value)} min={1} max={1000} />
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
    <Button icon={<Save size={14} />} disabled={invalidKeys.length > 0} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
  </SettingsSection>;
}
