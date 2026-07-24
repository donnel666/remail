import { useState, useCallback, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Button, Spin, TabPane, Tabs, Toast } from "@douyinfe/semi-ui";
import {
  getSystemOptions, updateSystemOption, updateSystemOptionsBulk,
  type SystemOption,
} from "@/lib/system-settings-api";
import { useAuth, hasPermissionKey } from "@/context/auth-provider";
import {
  Shield, Mail, Cpu, ShoppingCart, Cog, Users,
} from "lucide-react";

// Section components
import SiteContentSection from "./site-content";
import AuthSecuritySection from "./auth-security";
import EmailServiceSection from "./email-service";
import OrdersPaymentSection from "./orders-payment";
import SystemOperationsSection from "./system-operations";
import UsersRebatesSection from "./users-rebates";
import { SettingsAccessBoundary } from "./settings-layout";

export interface SectionProps {
  options: SystemOption[];
  loading: boolean;
  onSave: (key: string, value: string) => Promise<void>;
  onBulkSave: (updates: { key: string; value: string }[]) => Promise<void>;
  canWrite: boolean;
  canSensitive: boolean;
  canReadUserGroups: boolean;
  canWriteUserGroups: boolean;
}

type Section = {
  key: string;
  label: string;
  labelEn: string;
  icon: React.ReactNode;
  component: React.ComponentType<SectionProps>;
};

const SECTIONS: Section[] = [
  { key: "site-content", label: "内容管理", labelEn: "Content", icon: <Cog size={18} />, component: SiteContentSection },
  { key: "auth", label: "认证安全", labelEn: "Auth & Security", icon: <Shield size={18} />, component: AuthSecuritySection },
  { key: "email-service", label: "邮箱服务", labelEn: "Email Service", icon: <Mail size={18} />, component: EmailServiceSection },
  { key: "orders-payment", label: "订单支付", labelEn: "Orders & Payment", icon: <ShoppingCart size={18} />, component: OrdersPaymentSection },
  { key: "users-rebates", label: "用户返利", labelEn: "Users & Rebates", icon: <Users size={18} />, component: UsersRebatesSection },
  { key: "system-operations", label: "系统运维", labelEn: "System Operations", icon: <Cpu size={18} />, component: SystemOperationsSection },
];

export default function SystemSettingsPage() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const [options, setOptions] = useState<SystemOption[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [activeSection, setActiveSection] = useState(SECTIONS[0].key);

  const canAccess = Boolean(currentUser && hasPermissionKey(currentUser, "system:settings:read"));
  const canWrite = Boolean(currentUser && hasPermissionKey(currentUser, "system:settings:write"));
  const canSensitive = Boolean(currentUser && hasPermissionKey(currentUser, "system:settings:sensitive"));
  const canReadUserGroups = Boolean(currentUser && hasPermissionKey(currentUser, "iam:user_group:read"));
  const canWriteUserGroups = Boolean(currentUser && hasPermissionKey(currentUser, "iam:user_group:write"));

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const result = await getSystemOptions();
      setOptions(result.options ?? []);
    } catch (error) {
      const message = error instanceof Error ? error.message : t("加载系统设置失败");
      setLoadError(message);
      Toast.error(message);
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    if (canAccess) void load();
  }, [canAccess, load]);

  const handleSave = useCallback(async (key: string, value: string) => {
    setSaving(true);
    try {
      await updateSystemOption(key, value);
      setOptions((prev) => { const nx = prev.filter((o) => o.key !== key); nx.push({ key, value }); return nx; });
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : t("保存设置失败"));
      throw error;
    } finally {
      setSaving(false);
    }
  }, [t]);

  const handleBulkSave = useCallback(async (updates: { key: string; value: string }[]) => {
    setSaving(true);
    try {
      await updateSystemOptionsBulk(updates);
      setOptions((prev) => {
        const nx = [...prev];
        for (const up of updates) {
          const idx = nx.findIndex((o) => o.key === up.key);
          if (idx >= 0) nx[idx] = up; else nx.push(up);
        }
        return nx;
      });
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : t("保存设置失败"));
      throw error;
    } finally {
      setSaving(false);
    }
  }, [t]);

  if (!canAccess) {
    return <div className="flex h-64 items-center justify-center"><p className="text-muted-foreground">{t("Permission required: system settings")}</p></div>;
  }

  if (loadError) {
    return (
      <div className="console-content-width flex min-h-64 flex-col items-center justify-center gap-3 pt-3">
        <p className="text-sm text-[var(--semi-color-text-2)]">{t("系统设置加载失败")}：{loadError}</p>
        <Button onClick={() => void load()} theme="light" type="primary">{t("重试")}</Button>
      </div>
    );
  }

  return (
    <div className="console-content-width min-h-[calc(100vh-64px)] pt-3">
      <Tabs
        type="card"
        collapsible
        activeKey={activeSection}
        onChange={setActiveSection}
        tabPaneMotion={false}
      >
        {SECTIONS.map((section) => {
          const SectionComponent = section.component;
          const content = !loading ? <SectionComponent
            canReadUserGroups={canReadUserGroups}
            canSensitive={canSensitive}
            canWrite={canWrite}
            canWriteUserGroups={canWriteUserGroups}
            loading={saving}
            onBulkSave={handleBulkSave}
            onSave={handleSave}
            options={options}
          /> : null;
          return (
            <TabPane
              key={section.key}
              itemKey={section.key}
              tab={<span className="flex items-center gap-1.5">{section.icon}{t(section.label)}</span>}
            >
              <div hidden={activeSection !== section.key} className="min-h-40">
                <Spin spinning={loading} size="large">
                  {section.key === "users-rebates" || !content
                    ? content
                    : <SettingsAccessBoundary canWrite={canWrite}>{content}</SettingsAccessBoundary>}
                </Spin>
              </div>
            </TabPane>
          );
        })}
      </Tabs>
    </div>
  );
}
