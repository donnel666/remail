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
import { useNavigate } from "@tanstack/react-router";
import {
  BarChart2,
  Coins,
  ShieldCheck,
  UserPlus,
  Users,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import coverImage from "@/assets/cover-4.webp";
import { OverflowTooltip } from "@/components/semi/overflow-tooltip";
import { useAuth, type CurrentUser } from "@/context/auth-provider";
import { LOGIN_NOTICE_KEY, clearLoginReturnTo } from "@/lib/auth-flow";
import { changePassword } from "@/lib/iam-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { getAPIKeyUsage } from "@/lib/openapi-credentials-api";
import { getWallet, type WalletResponse } from "@/lib/wallet-api";

import { ApiKeyPanel } from "./account/api-key-panel";
import { ChangePasswordDialog } from "./account/change-password-dialog";
import { SettingItem } from "./account/setting-item";

const { Text } = Typography;

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

function formatCurrency(value: string | number | null | undefined) {
  if (value == null) return "-";
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return "-";
  return `￥${numeric.toLocaleString("zh-CN", {
    maximumFractionDigits: 6,
    minimumFractionDigits: 2,
  })}`;
}

function formatInteger(value: number | null) {
  if (value == null) return "-";
  return value.toLocaleString("zh-CN");
}

export default function Account() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { currentUser, logout } = useAuth();
  const [wallet, setWallet] = useState<WalletResponse | null>(null);
  const [requestCount, setRequestCount] = useState<number | null>(null);
  const [overviewLoading, setOverviewLoading] = useState(false);
  const [showChangePasswordModal, setShowChangePasswordModal] = useState(false);
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const displayName = currentUser?.nickname || currentUser?.name || "-";
  const roleLabel = t(getRoleLabel(currentUser?.role));
  const userGroupLabel = useMemo(() => {
    const group = currentUser?.userGroup;
    if (!group) return "-";
    if (group.code === "normal") return t("Normal User Group");
    return group.name || group.code || "-";
  }, [currentUser?.userGroup, t]);

  const refreshAccountOverview = useCallback(async () => {
    setOverviewLoading(true);
    try {
      const [walletResponse, nextRequestCount] = await Promise.all([
        getWallet(),
        getAPIKeyUsage().then((usage) => usage.requestCount),
      ]);
      setWallet(walletResponse);
      setRequestCount(nextRequestCount);
    } catch (nextError) {
      Toast.error(getIamErrorMessage(t, nextError, "Request failed."));
    } finally {
      setOverviewLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void refreshAccountOverview();
  }, [refreshAccountOverview]);

  const profileStats = useMemo(
    () => [
      {
        icon: <Coins size={16} />,
        label: "Historical Spend",
        value: overviewLoading ? "..." : formatCurrency(wallet?.historicalSpend),
      },
      {
        icon: <BarChart2 size={16} />,
        label: "Request Count",
        value: overviewLoading ? "..." : formatInteger(requestCount),
      },
      {
        icon: <Users size={16} />,
        label: "User Group",
        value: userGroupLabel,
      },
    ],
    [overviewLoading, requestCount, userGroupLabel, wallet?.historicalSpend]
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
              {overviewLoading ? "..." : formatCurrency(wallet?.consumerBalance)}
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
                    action={
                      <Button
                        onClick={handleMockOnly}
                        size="small"
                        theme="outline"
                        type="primary"
                      >
                        {t("Change Binding")}
                      </Button>
                    }
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
                    action={
                      <Button disabled size="small" theme="outline" type="tertiary">
                        {t("Not enabled")}
                      </Button>
                    }
                    description={t("Unbound")}
                    icon={<IconGithubLogo />}
                    title="GitHub"
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
                      <Button
                        icon={<IconLock />}
                        onClick={() => setShowChangePasswordModal(true)}
                        theme="solid"
                        type="primary"
                      >
                        {t("Change password")}
                      </Button>
                    }
                    description={t("Regularly changing your password improves account security.")}
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
    </div>
  );
}
