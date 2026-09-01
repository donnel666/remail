import {
  IconDelete,
  IconGithubLogo,
  IconLock,
  IconMail,
} from "@douyinfe/semi-icons";
import {
  Avatar,
  Badge,
  Button,
  Card,
  Divider,
  Space,
  Tabs,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  BarChart2,
  Coins,
  MessageCircle,
  ShieldCheck,
  UserPlus,
  Users,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import coverImage from "@/assets/cover-4.webp";
import { GitHubVerificationModal } from "@/components/auth/GitHubVerificationModal";
import { LinuxDoIcon } from "@/components/auth/LinuxDoIcon";
import { NodeLocIcon } from "@/components/auth/NodeLocIcon";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { useAuth, type CurrentUser } from "@/context/auth-provider";
import { LOGIN_NOTICE_KEY, clearLoginReturnTo } from "@/lib/auth-flow";
import {
  changePassword,
  githubBindURL,
  getLoginConfig,
  linuxDOBindURL,
  nodeLocBindURL,
} from "@/lib/iam-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { formatPriceMultiplier } from "@/lib/membership";
import { getAPIKeyUsage } from "@/lib/openapi-credentials-api";
import { formatPoints } from "@/lib/points";
import { getWallet, type WalletResponse } from "@/lib/wallet-api";

import { ApiKeyPanel } from "./account/api-key-panel";
import { ChangePasswordDialog } from "./account/change-password-dialog";
import { SettingItem } from "./account/setting-item";

const { Text } = Typography;

const oauthNoticeKeys: Record<string, string> = {
  github_bound: "GitHub account bound successfully.",
  linuxdo_bound: "LinuxDO account bound successfully.",
  nodeloc_bound: "NodeLoc account bound successfully.",
};

const oauthErrorKeys: Record<string, string> = {
  account: "LinuxDO account is unavailable.",
  already_bound: "This LinuxDO account is already bound.",
  cancelled: "LinuxDO authorization was cancelled.",
  disabled: "LinuxDO login is unavailable.",
  failed: "LinuxDO login failed.",
  rate_limited: "Too many LinuxDO login attempts. Please try again later.",
  session: "Your session expired. Please log in and try again.",
  state: "LinuxDO login request expired. Please try again.",
  trust_level: "Your LinuxDO trust level is too low.",
};

const githubOAuthErrorKeys: Record<string, string> = {
  account: "GitHub account is unavailable.",
  account_age: "Your GitHub account is too new.",
  already_bound: "This GitHub account is already bound.",
  cancelled: "GitHub authorization was cancelled.",
  disabled: "GitHub login is unavailable.",
  email: "Your GitHub account has no verified email.",
  failed: "GitHub login failed.",
  rate_limited: "Too many GitHub login attempts. Please try again later.",
  session: "Your session expired. Please log in and try again.",
  state: "GitHub login request expired. Please try again.",
};

const nodeLocOAuthErrorKeys: Record<string, string> = {
  account: "NodeLoc account is unavailable.",
  already_bound: "This NodeLoc account is already bound.",
  cancelled: "NodeLoc authorization was cancelled.",
  disabled: "NodeLoc login is unavailable.",
  failed: "NodeLoc login failed.",
  rate_limited: "Too many NodeLoc login attempts. Please try again later.",
  session: "Your session expired. Please log in and try again.",
  state: "NodeLoc login request expired. Please try again.",
  trust_level: "Your NodeLoc trust level is too low.",
};

type OAuthConfigState = "loading" | "enabled" | "disabled" | "error";

function getRoleLabel(role?: CurrentUser["role"]) {
  if (!role) return "Unknown";
  const roleLabels: Record<CurrentUser["role"], string> = {
    user: "User",
    supplier: "Supplier",
    admin: "Admin",
    super_admin: "Super Admin",
  };
  return roleLabels[role];
}

function getAvatarText(value?: string) {
  const normalized = value?.trim();
  if (!normalized) return "RM";
  return normalized.slice(0, 2).toUpperCase();
}

function formatInteger(value: number | null) {
  if (value == null) return "-";
  return value.toLocaleString("zh-CN");
}

export default function Account() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { currentUser, logout, refreshCurrentUser } = useAuth();
  const [wallet, setWallet] = useState<WalletResponse | null>(null);
  const [requestCount, setRequestCount] = useState<number | null>(null);
  const [overviewLoading, setOverviewLoading] = useState(false);
  const [linuxDOConfigState, setLinuxDOConfigState] = useState<OAuthConfigState>("loading");
  const [linuxDOBound, setLinuxDOBound] = useState(false);
  const [githubConfigState, setGitHubConfigState] = useState<OAuthConfigState>("loading");
  const [githubBound, setGitHubBound] = useState(false);
  const [nodeLocConfigState, setNodeLocConfigState] = useState<OAuthConfigState>("loading");
  const [nodeLocBound, setNodeLocBound] = useState(false);
  const [githubSetupOpen, setGitHubSetupOpen] = useState(false);
  const [oauthNotice, setOAuthNotice] = useState("");
  const [oauthError, setOAuthError] = useState("");
  const [showChangePasswordModal, setShowChangePasswordModal] = useState(false);
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const displayName = currentUser?.nickname || currentUser?.name || "-";
  const hasLocalPassword = currentUser?.hasLocalPassword ?? false;
  const roleLabel = t(getRoleLabel(currentUser?.role));
  const userGroupLabel = useMemo(() => {
    const group = currentUser?.userGroup;
    if (!group) return "-";
    if (group.code === "normal") return t("Normal User Group");
    return group.name || group.code || "-";
  }, [currentUser?.userGroup, t]);
  const userGroupSummary = useMemo(() => {
    const group = currentUser?.userGroup;
    if (!group) return "-";
    const concurrency = group.apiConcurrencyLimit > 0
      ? group.apiConcurrencyLimit.toLocaleString()
      : t("Uses API key or system limit");
    return [
      userGroupLabel,
      `${t("Multiplier")} ${formatPriceMultiplier(group.priceDiscountRatio)}`,
      `${t("Maximum concurrency")} ${concurrency}`,
    ].join(" · ");
  }, [currentUser?.userGroup, t, userGroupLabel]);

  const refreshAccountOverview = useCallback(async () => {
    setOverviewLoading(true);
    try {
      const [walletResponse, nextRequestCount] = await Promise.all([
        getWallet(),
        getAPIKeyUsage().then((usage) => usage.requestCount),
        refreshCurrentUser(),
      ]);
      setWallet(walletResponse);
      setRequestCount(nextRequestCount);
    } catch (nextError) {
      Toast.error(getIamErrorMessage(t, nextError, "Request failed."));
    } finally {
      setOverviewLoading(false);
    }
  }, [refreshCurrentUser, t]);

  useEffect(() => {
    void refreshAccountOverview();
  }, [refreshAccountOverview]);

  useEffect(() => {
    let cancelled = false;
    void getLoginConfig()
      .then((config) => {
        if (cancelled) return;
        setLinuxDOConfigState(config.linuxdoOAuthEnabled ? "enabled" : "disabled");
        setLinuxDOBound(config.linuxdoBound);
        setGitHubConfigState(config.githubOAuthEnabled ? "enabled" : "disabled");
        setGitHubBound(config.githubBound);
        setNodeLocConfigState(config.nodelocOAuthEnabled ? "enabled" : "disabled");
        setNodeLocBound(config.nodelocBound);
      })
      .catch(() => {
        if (cancelled) return;
        setLinuxDOConfigState("error");
        setGitHubConfigState("error");
        setNodeLocConfigState("error");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const notice = params.get("oauth_notice");
    const errorCode = params.get("oauth_error");
    const provider = params.get("oauth_provider");
    if (params.get("oauth_setup") === "github") setGitHubSetupOpen(true);
    if (notice && Object.prototype.hasOwnProperty.call(oauthNoticeKeys, notice)) setOAuthNotice(t(oauthNoticeKeys[notice]));
    if (errorCode) {
      const errorKeys = provider === "github" ? githubOAuthErrorKeys : provider === "nodeloc" ? nodeLocOAuthErrorKeys : oauthErrorKeys;
      const fallback = provider === "github" ? "GitHub login failed." : provider === "nodeloc" ? "NodeLoc login failed." : "LinuxDO login failed.";
      setOAuthError(t(Object.prototype.hasOwnProperty.call(errorKeys, errorCode) ? errorKeys[errorCode] : fallback));
    }
    if (!notice && !errorCode) return;
    params.delete("oauth_notice");
    params.delete("oauth_error");
    params.delete("oauth_provider");
    const search = params.toString();
    window.history.replaceState({}, "", window.location.pathname + (search ? `?${search}` : "") + window.location.hash);
  }, [t]);

  const profileStats = useMemo(
    () => [
      {
        icon: <Coins size={16} />,
        label: "Historical Spend",
        value: overviewLoading ? "..." : formatPoints(wallet?.historicalSpend),
      },
      {
        icon: <BarChart2 size={16} />,
        label: "Request Count",
        value: overviewLoading ? "..." : formatInteger(requestCount),
      },
      {
        icon: <Users size={16} />,
        label: "User Group",
        value: userGroupSummary,
      },
    ],
    [overviewLoading, requestCount, t, userGroupSummary, wallet?.historicalSpend]
  );

  const resetPasswordForm = () => {
    setOldPassword("");
    setNewPassword("");
    setConfirmPassword("");
    setError("");
  };

  const closeChangePasswordModal = () => {
    if (submitting) return;
    setShowChangePasswordModal(false);
    resetPasswordForm();
  };

  const handleChangePassword = async () => {
    if (!oldPassword.trim() || !newPassword.trim() || !confirmPassword.trim()) {
      setError(t("Please complete password fields."));
      return;
    }
    if (newPassword.length < 6) {
      setError(t("Password must be at least 6 characters."));
      return;
    }
    if (newPassword !== confirmPassword) {
      setError(t("Passwords do not match."));
      return;
    }

    setSubmitting(true);
    setError("");
    try {
      await changePassword({ oldPassword, newPassword });
      sessionStorage.setItem(
        LOGIN_NOTICE_KEY,
        "Password changed. Please log in again."
      );
      clearLoginReturnTo();
      await logout();
      void navigate({ to: "/login", replace: true });
    } catch (nextError) {
      setError(getIamErrorMessage(t, nextError, "Password change failed."));
    } finally {
      setSubmitting(false);
    }
  };

  const handleMockOnly = () => {
    Toast.info(t("Feature is not implemented yet."));
  };

  return (
    <div className="account-page console-content-width">
      {oauthNotice ? (
        <div className="mb-4 rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300" role="status">
          {oauthNotice}
        </div>
      ) : null}
      {oauthError ? (
        <div className="mb-4 rounded-lg border border-red-500/25 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300" role="alert">
          {oauthError}
        </div>
      ) : null}
      <Card
        bodyStyle={{ padding: 12 }}
        className="account-hero-card !rounded-2xl overflow-hidden"
        cover={
          <div
            className="account-hero-cover"
            style={{
              backgroundImage: `linear-gradient(0deg, rgba(96, 45, 13, 0.80), rgba(96, 45, 13, 0.80)), url(${coverImage})`,
            }}
          >
            <div className="account-hero-content">
              <Avatar
                className="account-hero-avatar"
                color="orange"
                size="large"
              >
                {getAvatarText(displayName)}
              </Avatar>
              <div className="account-hero-main">
                <OverflowTooltip content={displayName}>
                  <h1>{displayName}</h1>
                </OverflowTooltip>
                <div className="account-hero-tags">
                  <Tag shape="circle" size="large" style={{ color: "white" }}>
                    {roleLabel}
                  </Tag>
                  <Tag shape="circle" size="large" style={{ color: "white" }}>
                    ID: {currentUser?.id ?? "-"}
                  </Tag>
                </div>
              </div>
            </div>
          </div>
        }
      >
        <div className="account-hero-body">
          <Badge count={t("Current Balance")} position="rightTop" type="danger">
            <div className="account-hero-balance">
              {overviewLoading ? "..." : formatPoints(wallet?.consumerBalance)}
            </div>
          </Badge>

          <Card
            bodyStyle={{ padding: "12px 16px" }}
            className="account-hero-stat-card !rounded-xl"
          >
            <div className="account-hero-stats">
              {profileStats.map((stat, index) => (
                <div className="account-hero-stat" key={stat.label}>
                  {index !== 0 ? <Divider layout="vertical" /> : null}
                  <div className="account-hero-stat-content">
                    {stat.icon}
                    <Text size="small" type="tertiary">
                      {t(stat.label)}
                    </Text>
                    <Text size="small" strong type="tertiary">
                      <OverflowTooltip content={stat.value}>{stat.value}</OverflowTooltip>
                    </Text>
                  </div>
                </div>
              ))}
            </div>
          </Card>
        </div>
      </Card>

      <div className="account-content-grid">
        <Card className="account-management-card !rounded-2xl">
          <div className="account-card-header">
            <Avatar className="mr-3 shadow-md" color="teal" size="small">
              <UserPlus size={16} />
            </Avatar>
            <div>
              <Text className="text-lg font-medium">{t("Account Management")}</Text>
              <div className="text-xs text-[var(--semi-color-text-2)]">
                {t("Account binding, security settings and identity verification.")}
              </div>
            </div>
          </div>

          <Tabs defaultActiveKey="binding" type="card">
            <Tabs.TabPane
              itemKey="binding"
              tab={
                <div className="account-tab-title">
                  <UserPlus size={16} />
                  {t("Account Binding")}
                </div>
              }
            >
              <div className="account-tab-body">
                <div className="account-binding-grid">
                  <SettingItem
                    description={
                      <OverflowTooltip content={currentUser?.email || "-"}>
                        {currentUser?.email || "-"}
                      </OverflowTooltip>
                    }
                    icon={<IconMail />}
                    iconTone="orange"
                    title={t("Email")}
                  />
                  <SettingItem
                    description={
                      <OverflowTooltip content={currentUser?.qqNumber || t("Unbound")}>
                        {currentUser?.qqNumber || t("Unbound")}
                      </OverflowTooltip>
                    }
                    icon={<MessageCircle size={20} />}
                    iconTone="green"
                    title={t("QQ number")}
                  />
                  <SettingItem
                    action={
                      linuxDOConfigState === "loading" ? (
                        <Button className="min-h-11" disabled loading size="small" theme="outline" type="tertiary">
                          {t("Loading...")}
                        </Button>
                      ) : linuxDOConfigState === "error" ? (
                        <Button className="min-h-11" disabled size="small" theme="outline" type="tertiary">
                          {t("Unavailable")}
                        </Button>
                      ) : linuxDOConfigState === "enabled" && !linuxDOBound ? (
                        <a
                          className="inline-flex min-h-11 items-center justify-center rounded-lg border border-[var(--semi-color-primary)] px-4 text-sm font-medium text-[var(--semi-color-primary)] transition-colors hover:bg-[var(--semi-color-primary-light-default)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--semi-color-primary)] focus-visible:ring-offset-2"
                          href={linuxDOBindURL}
                        >
                          {t("Bind")}
                        </a>
                      ) : (
                        <Button className="min-h-11" disabled size="small" theme="outline" type="tertiary">
                          {linuxDOBound ? t("Bound") : t("Not enabled")}
                        </Button>
                      )
                    }
                    description={
                      linuxDOConfigState === "loading"
                        ? t("Loading LinuxDO account status...")
                        : linuxDOConfigState === "error"
                          ? <span role="alert">{t("Could not load LinuxDO account status. Please try again later.")}</span>
                          : linuxDOBound ? t("Bound to LinuxDO") : t("Unbound")
                    }
                    icon={<LinuxDoIcon className="size-5" />}
                    iconTone="violet"
                    title="LinuxDO"
                  />
                  <SettingItem
                    action={
                      githubConfigState === "loading" ? (
                        <Button className="min-h-11" disabled loading size="small" theme="outline" type="tertiary">{t("Loading...")}</Button>
                      ) : githubConfigState === "error" ? (
                        <Button className="min-h-11" disabled size="small" theme="outline" type="tertiary">{t("Unavailable")}</Button>
                      ) : githubConfigState === "enabled" && !githubBound ? (
                        <a className="inline-flex min-h-11 items-center justify-center rounded-lg border border-[var(--semi-color-primary)] px-4 text-sm font-medium text-[var(--semi-color-primary)] transition-colors hover:bg-[var(--semi-color-primary-light-default)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--semi-color-primary)] focus-visible:ring-offset-2" href={githubBindURL}>{t("Bind")}</a>
                      ) : (
                        <Button className="min-h-11" disabled size="small" theme="outline" type="tertiary">{githubBound ? t("Bound") : t("Not enabled")}</Button>
                      )
                    }
                    description={
                      githubConfigState === "loading"
                        ? t("Loading GitHub account status...")
                        : githubConfigState === "error"
                          ? <span role="alert">{t("Could not load GitHub account status. Please try again later.")}</span>
                          : githubBound ? t("Bound to GitHub") : t("Unbound")
                    }
                    icon={<IconGithubLogo />}
                    title="GitHub"
                  />
                  <SettingItem
                    action={
                      nodeLocConfigState === "loading" ? (
                        <Button className="min-h-11" disabled loading size="small" theme="outline" type="tertiary">{t("Loading...")}</Button>
                      ) : nodeLocConfigState === "error" ? (
                        <Button className="min-h-11" disabled size="small" theme="outline" type="tertiary">{t("Unavailable")}</Button>
                      ) : nodeLocConfigState === "enabled" && !nodeLocBound ? (
                        <a className="inline-flex min-h-11 items-center justify-center rounded-lg border border-[var(--semi-color-primary)] px-4 text-sm font-medium text-[var(--semi-color-primary)] transition-colors hover:bg-[var(--semi-color-primary-light-default)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--semi-color-primary)] focus-visible:ring-offset-2" href={nodeLocBindURL}>{t("Bind")}</a>
                      ) : (
                        <Button className="min-h-11" disabled size="small" theme="outline" type="tertiary">{nodeLocBound ? t("Bound") : t("Not enabled")}</Button>
                      )
                    }
                    description={
                      nodeLocConfigState === "loading"
                        ? t("Loading NodeLoc account status...")
                        : nodeLocConfigState === "error"
                          ? <span role="alert">{t("Could not load NodeLoc account status. Please try again later.")}</span>
                          : nodeLocBound ? t("Bound to NodeLoc") : t("Unbound")
                    }
                    icon={<NodeLocIcon className="size-[18px]" />}
                    iconTone="violet"
                    title="NodeLoc"
                  />
                </div>
              </div>
            </Tabs.TabPane>

            <Tabs.TabPane
              itemKey="security"
              tab={
                <div className="account-tab-title">
                  <ShieldCheck size={16} />
                  {t("Security Settings")}
                </div>
              }
            >
              <div className="account-tab-body">
                <Space className="w-full" vertical>
                  <SettingItem
                    action={
                      hasLocalPassword ? (
                        <Button
                          icon={<IconLock />}
                          onClick={() => setShowChangePasswordModal(true)}
                          theme="solid"
                          type="primary"
                        >
                          {t("Change password")}
                        </Button>
                      ) : (
                        <Link
                          className="inline-flex min-h-11 items-center justify-center gap-2 rounded-lg border border-[var(--semi-color-primary)] px-4 text-sm font-medium text-[var(--semi-color-primary)] transition-colors hover:bg-[var(--semi-color-primary-light-default)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--semi-color-primary)] focus-visible:ring-offset-2"
                          to="/password-reset"
                        >
                          <IconLock aria-hidden="true" />
                          {t("Set login password")}
                        </Link>
                      )
                    }
                    description={t(hasLocalPassword ? "Regularly changing your password improves account security." : "Set a password through email verification if you also want to sign in with email and password.")}
                    icon={<IconLock />}
                    iconTone="orange"
                    title={t("Password Management")}
                  />
                  <SettingItem
                    action={
                      <Button
                        icon={<IconDelete />}
                        onClick={handleMockOnly}
                        theme="outline"
                        type="danger"
                      >
                        {t("Delete Account")}
                      </Button>
                    }
                    description={t("This operation cannot be undone.")}
                    icon={<IconDelete />}
                    iconTone="orange"
                    title={t("Delete Account")}
                  />
                </Space>
              </div>
            </Tabs.TabPane>
          </Tabs>
        </Card>

        <ApiKeyPanel />
      </div>

      <ChangePasswordDialog
        confirmPassword={confirmPassword}
        error={error}
        newPassword={newPassword}
        oldPassword={oldPassword}
        onCancel={closeChangePasswordModal}
        onConfirm={handleChangePassword}
        onConfirmPasswordChange={setConfirmPassword}
        onNewPasswordChange={setNewPassword}
        onOldPasswordChange={setOldPassword}
        open={showChangePasswordModal}
        submitting={submitting}
      />
      <GitHubVerificationModal
        onCancel={() => setGitHubSetupOpen(false)}
        onComplete={async (intent) => {
          setGitHubSetupOpen(false);
          if (intent === "bind") {
            setGitHubBound(true);
            setOAuthError("");
            setOAuthNotice(t("GitHub account bound successfully."));
            return;
          }
          await refreshCurrentUser();
        }}
        open={githubSetupOpen}
      />
    </div>
  );
}
