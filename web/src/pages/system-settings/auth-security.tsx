import { useState } from "react";
import { Button, TagInput, Toast } from "@douyinfe/semi-ui";
import { Copy, ExternalLink, Github, Save, ShieldAlert, UserPlus } from "lucide-react";
import { useTranslation } from "react-i18next";

import { LinuxDoIcon } from "@/components/auth/LinuxDoIcon";
import { NodeLocIcon } from "@/components/auth/NodeLocIcon";
import { copyText } from "@/lib/clipboard";
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
  registration_email_whitelist: "qq.com,foxmail.com,gmail.com,proton.me,protonmail.com,pm.me,mail.com",
  registration_reward_amount: 0,
  password_login_enabled: true,
  captcha_enabled: true,
  github_oauth_enabled: false,
  github_client_id: "",
  github_client_secret: "",
  github_callback_url: "",
  github_minimum_account_age_days: 0,
  nodeloc_oauth_enabled: false,
  nodeloc_client_id: "",
  nodeloc_client_secret: "",
  nodeloc_callback_url: "https://remail.aishop6.com/oauth/nodeloc",
  nodeloc_minimum_trust_level: 0,
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
  linuxdo_oauth_enabled: false,
  linuxdo_client_id: "",
  linuxdo_client_secret: "",
  linuxdo_callback_url: "",
  linuxdo_minimum_trust_level: 0,
};

const TWO_COLUMN_GRID = "xl:grid-cols-2 xl:[&>[data-settings-form-span=full]]:col-span-2 xl:[&>[data-slot=form-item]:has(textarea)]:col-span-2";
export default function AuthSecuritySection({ options, onBulkSave, canSensitive }: SectionProps) {
  const { t } = useTranslation();
  const linuxDOCallbackURL = typeof window === "undefined" ? "" : window.location.origin + "/v1/oauth/linuxdo/callback";
  const githubCallbackURL = typeof window === "undefined" ? "" : window.location.origin + "/v1/oauth/github/callback";
  const nodeLocCallbackURL = "https://remail.aishop6.com/oauth/nodeloc";
  const [form, setForm] = useState(() => {
    const values = { ...(parseOption(options, D as any) as Record<string, unknown>) };
    values.linuxdo_client_id = "";
    values.linuxdo_client_secret = "";
    values.github_client_id = "";
    values.github_client_secret = "";
    values.nodeloc_client_id = "";
    values.nodeloc_client_secret = "";
    if (!canSensitive || !String(values.linuxdo_callback_url ?? "").trim()) values.linuxdo_callback_url = linuxDOCallbackURL;
    if (!canSensitive || !String(values.github_callback_url ?? "").trim()) values.github_callback_url = githubCallbackURL;
    if (!canSensitive || !String(values.nodeloc_callback_url ?? "").trim()) values.nodeloc_callback_url = nodeLocCallbackURL;
    return values;
  });
  const [savingCard, setSavingCard] = useState<string | null>(null);
  const update = (key: string, value: unknown) => setForm((current) => ({ ...current, [key]: value }));
  const number = (value: unknown) => Number(value) || 0;
  const whitelistDomains = String(form.registration_email_whitelist ?? "").split(/[\s,，]+/).map((domain) => domain.trim()).filter(Boolean);

  const saveCard = async (card: string, keys: string[]) => {
    setSavingCard(card);
    try {
      await onBulkSave(keys.flatMap((key) => {
        const value = String(form[key] ?? "");
        return (key.endsWith("_client_id") || key.endsWith("_client_secret")) && !value.trim() ? [] : [{ key, value }];
      }));
      if (card === "linuxdo" || card === "github" || card === "nodeloc") {
        setForm((current) => ({
          ...current,
          [`${card}_client_id`]: "",
          [`${card}_client_secret`]: "",
        }));
      }
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
        <SettingsNumberField label={t("新用户注册奖励积分")} value={number(form.registration_reward_amount)} onChange={(value) => update("registration_reward_amount", value)} min={0} precision={6} step={0.01} />
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "register"} onClick={() => void saveCard("register", ["register_enabled", "password_login_enabled", "captcha_enabled", "registration_email_whitelist", "registration_reward_amount"]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader
      icon={<LinuxDoIcon className="size-4" />}
      title={t("LinuxDO third-party login")}
      description={t("Configure LinuxDO Connect for verified account login and binding")}
      enabled={!!form.linuxdo_oauth_enabled}
      onToggle={(value) => update("linuxdo_oauth_enabled", value)}
      statusText={form.linuxdo_oauth_enabled ? t("已启用") : t("已禁用")}
    />}>
      <SettingsFormGrid className={`${TWO_COLUMN_GRID} mt-4`}>
        <div data-settings-form-span="full" className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div>
              <p className="text-sm font-medium text-[var(--semi-color-text-0)]">{t("Setup guide")}</p>
              <p className="mt-1 text-xs leading-5 text-[var(--semi-color-text-2)]">{t("Set this callback URL in your LinuxDO Connect application before enabling login.")}</p>
            </div>
            <a
              href="https://connect.linux.do/"
              target="_blank"
              rel="noreferrer"
              className="inline-flex min-h-11 shrink-0 cursor-pointer items-center gap-1 text-sm font-medium text-brand hover:text-brand-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--brand-start)]"
            >
              {t("Manage your LinuxDO OAuth app")}
              <ExternalLink size={14} aria-hidden="true" />
            </a>
          </div>
          <div className="mt-3 flex min-w-0 flex-col gap-2 rounded-md bg-[var(--semi-color-bg-0)] p-3 sm:flex-row sm:items-center">
            <span className="shrink-0 text-xs text-[var(--semi-color-text-2)]">{t("Authorization callback URL")}</span>
            <code className="min-w-0 flex-1 break-all text-xs text-[var(--semi-color-text-0)]">{String(form.linuxdo_callback_url)}</code>
            <Button
              aria-label={t("Copy callback URL")}
              icon={<Copy size={14} />}
              onClick={() => void copyText(String(form.linuxdo_callback_url)).then(() => Toast.success(t("Copied"))).catch(() => Toast.error(t("Copy failed.")))}
              theme="borderless"
              type="tertiary"
              className="min-h-11 min-w-11"
            />
          </div>
        </div>
        <SettingsTextField label="Client ID" value={String(form.linuxdo_client_id)} onChange={(value) => update("linuxdo_client_id", value)} disabled={!canSensitive} placeholder={canSensitive ? t("Saved client ID is not shown; leave blank to keep it unchanged") : t("需要敏感设置权限")} />
        <SettingsTextField label="Client Secret" value={String(form.linuxdo_client_secret)} onChange={(value) => update("linuxdo_client_secret", value)} type="password" disabled={!canSensitive} placeholder={canSensitive ? t("Saved secret is not shown; leave blank to keep it unchanged") : t("需要敏感设置权限")} />
        <SettingsNumberField label={t("Minimum Trust Level")} description={t("Minimum LinuxDO trust level required (0-4)")} value={number(form.linuxdo_minimum_trust_level)} onChange={(value) => update("linuxdo_minimum_trust_level", value)} min={0} max={4} />
        <div data-settings-form-span="full">
          <SettingsTextField label={t("Authorization callback URL")} value={String(form.linuxdo_callback_url)} onChange={(value) => update("linuxdo_callback_url", value)} disabled={!canSensitive} placeholder={linuxDOCallbackURL} />
        </div>
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "linuxdo"} onClick={() => void saveCard("linuxdo", ["linuxdo_oauth_enabled", ...(canSensitive ? ["linuxdo_client_id", "linuxdo_client_secret", "linuxdo_callback_url"] : []), "linuxdo_minimum_trust_level"]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader
      icon={<NodeLocIcon className="size-4" />}
      title={t("NodeLoc third-party login")}
      description={t("Configure NodeLoc OAuth for verified account login and binding")}
      enabled={!!form.nodeloc_oauth_enabled}
      onToggle={(value) => update("nodeloc_oauth_enabled", value)}
      statusText={form.nodeloc_oauth_enabled ? t("已启用") : t("已禁用")}
    />}>
      <SettingsFormGrid className={`${TWO_COLUMN_GRID} mt-4`}>
        <div data-settings-form-span="full" className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div>
              <p className="text-sm font-medium text-[var(--semi-color-text-0)]">{t("Setup guide")}</p>
              <p className="mt-1 text-xs leading-5 text-[var(--semi-color-text-2)]">{t("Set this callback URL in your NodeLoc OAuth application and request email scope approval before enabling login.")}</p>
            </div>
            <a href="https://www.nodeloc.com/oauth-provider/applications" target="_blank" rel="noreferrer" className="inline-flex min-h-11 shrink-0 cursor-pointer items-center gap-1 text-sm font-medium text-brand hover:text-brand-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--brand-start)]">
              {t("Manage your NodeLoc OAuth app")}
              <ExternalLink size={14} aria-hidden="true" />
            </a>
          </div>
          <div className="mt-3 flex min-w-0 flex-col gap-2 rounded-md bg-[var(--semi-color-bg-0)] p-3 sm:flex-row sm:items-center">
            <span className="shrink-0 text-xs text-[var(--semi-color-text-2)]">{t("Authorization callback URL")}</span>
            <code className="min-w-0 flex-1 break-all text-xs text-[var(--semi-color-text-0)]">{String(form.nodeloc_callback_url)}</code>
            <Button aria-label={t("Copy callback URL")} icon={<Copy size={14} />} onClick={() => void copyText(String(form.nodeloc_callback_url)).then(() => Toast.success(t("Copied"))).catch(() => Toast.error(t("Copy failed.")))} theme="borderless" type="tertiary" className="min-h-11 min-w-11" />
          </div>
        </div>
        <SettingsTextField label="Client ID" value={String(form.nodeloc_client_id)} onChange={(value) => update("nodeloc_client_id", value)} disabled={!canSensitive} placeholder={canSensitive ? t("Saved client ID is not shown; leave blank to keep it unchanged") : t("需要敏感设置权限")} />
        <SettingsTextField label="Client Secret" value={String(form.nodeloc_client_secret)} onChange={(value) => update("nodeloc_client_secret", value)} type="password" disabled={!canSensitive} placeholder={canSensitive ? t("Saved secret is not shown; leave blank to keep it unchanged") : t("需要敏感设置权限")} />
        <SettingsNumberField label={t("Minimum Trust Level")} description={t("Minimum NodeLoc trust level required (0-4)")} value={number(form.nodeloc_minimum_trust_level)} onChange={(value) => update("nodeloc_minimum_trust_level", value)} min={0} max={4} />
        <div data-settings-form-span="full">
          <SettingsTextField label={t("Authorization callback URL")} value={String(form.nodeloc_callback_url)} onChange={(value) => update("nodeloc_callback_url", value)} disabled={!canSensitive} placeholder={nodeLocCallbackURL} />
        </div>
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "nodeloc"} onClick={() => void saveCard("nodeloc", ["nodeloc_oauth_enabled", ...(canSensitive ? ["nodeloc_client_id", "nodeloc_client_secret", "nodeloc_callback_url"] : []), "nodeloc_minimum_trust_level"]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader
      icon={<Github size={16} />}
      title={t("GitHub third-party login")}
      description={t("Configure GitHub OAuth App for verified email login and registration")}
      enabled={!!form.github_oauth_enabled}
      onToggle={(value) => update("github_oauth_enabled", value)}
      statusText={form.github_oauth_enabled ? t("已启用") : t("已禁用")}
    />}>
      <SettingsFormGrid className={`${TWO_COLUMN_GRID} mt-4`}>
        <div data-settings-form-span="full" className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
            <div>
              <p className="text-sm font-medium text-[var(--semi-color-text-0)]">{t("Setup guide")}</p>
              <p className="mt-1 text-xs leading-5 text-[var(--semi-color-text-2)]">{t("Set this callback URL in your GitHub OAuth App before enabling login.")}</p>
            </div>
            <a href="https://github.com/settings/developers" target="_blank" rel="noreferrer" className="inline-flex min-h-11 shrink-0 cursor-pointer items-center gap-1 text-sm font-medium text-brand hover:text-brand-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--brand-start)]">
              {t("Manage your GitHub OAuth App")}
              <ExternalLink size={14} aria-hidden="true" />
            </a>
          </div>
          <div className="mt-3 flex min-w-0 flex-col gap-2 rounded-md bg-[var(--semi-color-bg-0)] p-3 sm:flex-row sm:items-center">
            <span className="shrink-0 text-xs text-[var(--semi-color-text-2)]">{t("Authorization callback URL")}</span>
            <code className="min-w-0 flex-1 break-all text-xs text-[var(--semi-color-text-0)]">{String(form.github_callback_url)}</code>
            <Button aria-label={t("Copy callback URL")} icon={<Copy size={14} />} onClick={() => void copyText(String(form.github_callback_url)).then(() => Toast.success(t("Copied"))).catch(() => Toast.error(t("Copy failed.")))} theme="borderless" type="tertiary" className="min-h-11 min-w-11" />
          </div>
        </div>
        <SettingsTextField label="Client ID" value={String(form.github_client_id)} onChange={(value) => update("github_client_id", value)} disabled={!canSensitive} placeholder={canSensitive ? t("Saved client ID is not shown; leave blank to keep it unchanged") : t("需要敏感设置权限")} />
        <SettingsTextField label="Client Secret" value={String(form.github_client_secret)} onChange={(value) => update("github_client_secret", value)} type="password" disabled={!canSensitive} placeholder={canSensitive ? t("Saved secret is not shown; leave blank to keep it unchanged") : t("需要敏感设置权限")} />
        <SettingsNumberField label={t("Minimum GitHub account age (days)")} description={t("Set to 365 to require an account at least one year old; 0 disables the limit")} value={number(form.github_minimum_account_age_days)} onChange={(value) => update("github_minimum_account_age_days", value)} min={0} max={36500} precision={0} step={1} />
        <div data-settings-form-span="full">
          <SettingsTextField label={t("Authorization callback URL")} value={String(form.github_callback_url)} onChange={(value) => update("github_callback_url", value)} disabled={!canSensitive} placeholder={githubCallbackURL} />
        </div>
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "github"} onClick={() => void saveCard("github", ["github_oauth_enabled", ...(canSensitive ? ["github_client_id", "github_client_secret", "github_callback_url"] : []), "github_minimum_account_age_days"]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>

    <SettingsSection title={<SettingsCardHeader
      icon={<ShieldAlert size={16} />}
      title={t("安全策略")}
      description={t("配置登录防暴力破解、验证码防滥用、密码哈希和会话有效期")}
    />}>
      <SettingsFormGrid className="mt-4">
        <SettingsNumberField label={t("单邮箱登录限制次数")} value={number(form.login_email_limit)} onChange={(value) => update("login_email_limit", value)} min={1} max={1000} />
        <SettingsNumberField label={t("单IP登录限制次数")} value={number(form.login_ip_limit)} onChange={(value) => update("login_ip_limit", value)} min={1} max={10000} />
        <SettingsNumberField label={t("登录频率统计窗口（秒）")} value={number(form.login_window_seconds)} onChange={(value) => update("login_window_seconds", value)} min={1} max={86400} />
        <SettingsNumberField label={t("单邮箱验证码校验失败限制")} value={number(form.email_code_email_limit)} onChange={(value) => update("email_code_email_limit", value)} min={1} max={1000} />
        <SettingsNumberField label={t("单IP验证码校验失败限制")} value={number(form.email_code_ip_limit)} onChange={(value) => update("email_code_ip_limit", value)} min={1} max={10000} />
        <SettingsNumberField label={t("验证码失败统计窗口（秒）")} value={number(form.email_code_window_seconds)} onChange={(value) => update("email_code_window_seconds", value)} min={1} max={86400} />
        <SettingsNumberField label={t("人机验证频率限制（次/60秒）")} value={number(form.captcha_rate_limit)} onChange={(value) => update("captcha_rate_limit", value)} min={1} max={10000} />
        <SettingsNumberField label={t("验证码有效期（秒）")} value={number(form.email_code_ttl_seconds)} onChange={(value) => update("email_code_ttl_seconds", value)} min={1} max={86400} />
        <SettingsNumberField label={t("验证码重发间隔（秒）")} value={number(form.email_code_resend_gap_seconds)} onChange={(value) => update("email_code_resend_gap_seconds", value)} min={1} max={3600} />
        <SettingsNumberField label={t("验证码位数")} value={number(form.email_code_digit_len)} onChange={(value) => update("email_code_digit_len", value)} min={4} max={10} />
        <SettingsNumberField label={t("密码哈希成本（bcrypt cost）")} value={number(form.bcrypt_cost)} onChange={(value) => update("bcrypt_cost", value)} min={4} max={16} />
        <SettingsNumberField label={t("会话有效期（秒）")} value={number(form.session_max_age_seconds)} onChange={(value) => update("session_max_age_seconds", value)} min={300} max={31536000} />
      </SettingsFormGrid>
      <Button icon={<Save size={14} />} loading={savingCard === "security"} onClick={() => void saveCard("security", ["login_email_limit", "login_ip_limit", "login_window_seconds", "email_code_email_limit", "email_code_ip_limit", "email_code_window_seconds", "captcha_rate_limit", "email_code_ttl_seconds", "email_code_resend_gap_seconds", "email_code_digit_len", "bcrypt_cost", "session_max_age_seconds"]).catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsSection>
  </div>;
}
