import { useState } from "react";
import { Button, TagInput } from "@douyinfe/semi-ui";
import { Github, Save, ShieldAlert, UserPlus } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import {
  FormItem,
  FormLabel,
  SettingsCardHeader,
  SettingsFormGrid,
  SettingsNumberField,
  SettingsSection,
  SettingsSwitchField,
  SettingsTextField,
} from "./settings-layout";

const D: Record<string, unknown> = {
  register_enabled: true,
  registration_email_whitelist: "qq.com,gmail.com,...",
  registration_reward_amount: 0,
  password_login_enabled: true,
  captcha_enabled: true,
  github_oauth_enabled: false,
  github_client_id: "",
  github_client_secret: "",
  github_callback_url: "",
  login_email_limit: 10,
  login_ip_limit: 60,
  login_window_seconds: 900,
  email_code_email_limit: 5,
  email_code_ip_limit: 30,
  email_code_window_seconds: 600,
  captcha_rate_limit: 30,
  email_code_ttl_seconds: 600,
  email_code_resend_gap_seconds: 60,
  email_code_digit_len: 6,
  bcrypt_cost: 12,
  session_max_age_seconds: 86400,
};

const TWO_COLUMN_GRID = "xl:grid-cols-2 xl:[&>[data-settings-form-span=full]]:col-span-2 xl:[&>[data-slot=form-item]:has(textarea)]:col-span-2";
export default function AuthSecuritySection({ options, onBulkSave, canSensitive }: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(parseOption(options, D as any) as Record<string, unknown>);
  const [savingCard, setSavingCard] = useState<string | null>(null);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const whitelistDomains = String(form.registration_email_whitelist ?? "").split(/[\s,，]+/).map((domain) => domain.trim()).filter(Boolean);

  const saveCard = async (card: string, keys: string[]) => {
    setSavingCard(card);
    try {
      await onBulkSave(keys.map((key) => ({ key, value: String(form[key] ?? "") })));
    } finally {
      setSavingCard(null);
    }
  };

  return <div className="space-y-6">
    <SettingsSection title={<SettingsCardHeader
      icon={<UserPlus size={16} />}
      title={t("注册与登录开关")}
      description={t("控制新用户注册、密码登录和人机验证方式")}
    />}>
      <SettingsFormGrid className={`${TWO_COLUMN_GRID} mt-4`}>
        <SettingsSwitchField checked={!!form.register_enabled} onChange={(value) => update("register_enabled", value)} label={t("允许新用户注册")} description={t("关闭后只能通过邀请链接注册")} />
        <SettingsSwitchField checked={!!form.password_login_enabled} onChange={(value) => update("password_login_enabled", value)} label={t("允许密码登录")} description={t("关闭后只能用验证码或第三方登录")} />
        <SettingsSwitchField checked={!!form.captcha_enabled} onChange={(value) => update("captcha_enabled", value)} label={t("开启人机验证")} description={t("Cloudflare Turnstile 验证码开关")} />
        <FormItem spanFull>
          <FormLabel>{t("注册邮箱域名白名单")}</FormLabel>
          <TagInput
            aria-label={t("注册邮箱域名白名单")}
            value={whitelistDomains}
            separator={[",", "，", " ", "\n"]}
            allowDuplicates={false}
            addOnBlur
            showClear
            placeholder={t("输入邮箱域名后回车，如 gmail.com")}
            onChange={(domains) => update("registration_email_whitelist", domains.map((domain) => domain.trim()).filter(Boolean).join(","))}
            style={{ width: "100%" }}
          />
          <p className="text-xs text-[var(--semi-color-text-2)]">{t("每个域名单独显示；输入后按回车添加，点击标签上的关闭按钮删除")}</p>
        </FormItem>
        <SettingsNumberField label={t("新用户注册奖励金额")} value={number(form.registration_reward_amount)} onChange={(value) => update("registration_reward_amount", value)} min={0} />
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "register"} onClick={() => void saveCard("register", ["register_enabled", "password_login_enabled", "captcha_enabled", "registration_email_whitelist", "registration_reward_amount"]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader
      icon={<Github size={16} />}
      title={t("GitHub 第三方登录")}
      description={t("配置 GitHub OAuth App，用于用户登录和注册")}
      enabled={!!form.github_oauth_enabled}
      onToggle={(value) => update("github_oauth_enabled", value)}
      statusText={form.github_oauth_enabled ? t("已启用") : t("已禁用")}
    />}>
      <SettingsFormGrid className={`${TWO_COLUMN_GRID} mt-4`}>
        <SettingsTextField label="Client ID" value={String(form.github_client_id)} onChange={(value) => update("github_client_id", value)} />
        <SettingsTextField label="Client Secret" value={String(form.github_client_secret)} onChange={(value) => update("github_client_secret", value)} type="password" disabled={!canSensitive} placeholder={canSensitive ? t("敏感信息不会直接显示") : t("需要敏感设置权限")} />
        <div data-settings-form-span="full">
          <SettingsTextField label={t("回调地址")} value={String(form.github_callback_url)} onChange={(value) => update("github_callback_url", value)} placeholder="https://example.com/oauth/github" />
        </div>
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "github"} onClick={() => void saveCard("github", ["github_oauth_enabled", "github_client_id", ...(canSensitive ? ["github_client_secret"] : []), "github_callback_url"]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader
      icon={<ShieldAlert size={16} />}
      title={t("安全策略")}
      description={t("配置登录防暴力破解、验证码防滥用、密码哈希和会话有效期")}
    />}>
      <SettingsFormGrid className="mt-4">
        <SettingsNumberField label={t("单邮箱登录限制次数")} value={number(form.login_email_limit)} onChange={(value) => update("login_email_limit", value)} min={0} />
        <SettingsNumberField label={t("单IP登录限制次数")} value={number(form.login_ip_limit)} onChange={(value) => update("login_ip_limit", value)} min={0} />
        <SettingsNumberField label={t("登录频率统计窗口（秒）")} value={number(form.login_window_seconds)} onChange={(value) => update("login_window_seconds", value)} min={0} />
        <SettingsNumberField label={t("单邮箱验证码发送限制")} value={number(form.email_code_email_limit)} onChange={(value) => update("email_code_email_limit", value)} min={0} />
        <SettingsNumberField label={t("单IP验证码发送限制")} value={number(form.email_code_ip_limit)} onChange={(value) => update("email_code_ip_limit", value)} min={0} />
        <SettingsNumberField label={t("验证码频率统计窗口（秒）")} value={number(form.email_code_window_seconds)} onChange={(value) => update("email_code_window_seconds", value)} min={0} />
        <SettingsNumberField label={t("人机验证频率限制（次/60秒）")} value={number(form.captcha_rate_limit)} onChange={(value) => update("captcha_rate_limit", value)} min={0} />
        <SettingsNumberField label={t("验证码有效期（秒）")} value={number(form.email_code_ttl_seconds)} onChange={(value) => update("email_code_ttl_seconds", value)} min={0} />
        <SettingsNumberField label={t("验证码重发间隔（秒）")} value={number(form.email_code_resend_gap_seconds)} onChange={(value) => update("email_code_resend_gap_seconds", value)} min={0} />
        <SettingsNumberField label={t("验证码位数")} value={number(form.email_code_digit_len)} onChange={(value) => update("email_code_digit_len", value)} min={0} />
        <SettingsNumberField label={t("密码哈希成本（bcrypt cost）")} value={number(form.bcrypt_cost)} onChange={(value) => update("bcrypt_cost", value)} min={0} />
        <SettingsNumberField label={t("会话有效期（秒）")} value={number(form.session_max_age_seconds)} onChange={(value) => update("session_max_age_seconds", value)} min={0} />
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "security"} onClick={() => void saveCard("security", ["login_email_limit", "login_ip_limit", "login_window_seconds", "email_code_email_limit", "email_code_ip_limit", "email_code_window_seconds", "captcha_rate_limit", "email_code_ttl_seconds", "email_code_resend_gap_seconds", "email_code_digit_len", "bcrypt_cost", "session_max_age_seconds"]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>
  </div>;
}
