import { useState, useCallback, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Spin, TabPane, Tabs } from "@douyinfe/semi-ui";
import {
  getSystemOptions, updateSystemOption, updateSystemOptionsBulk,
  type SystemOption,
} from "@/lib/settings-api-mock";
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

export interface SectionProps {
  options: SystemOption[];
  loading: boolean;
  onSave: (key: string, value: string) => Promise<void>;
  onBulkSave: (updates: { key: string; value: string }[]) => Promise<void>;
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
  const [saving, setSaving] = useState(false);
  const [activeSection, setActiveSection] = useState(SECTIONS[0].key);

  const canAccess = currentUser && hasPermissionKey(currentUser, "iam:permission:read");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const result = await getSystemOptions();
      setOptions(result.options ?? []);
    } finally { setLoading(false); }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const handleSave = useCallback(async (key: string, value: string) => {
    setSaving(true);
    try {
      await updateSystemOption(key, value);
      setOptions((prev) => { const nx = prev.filter((o) => o.key !== key); nx.push({ key, value }); return nx; });
    } finally { setSaving(false); }
  }, []);

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
    } finally { setSaving(false); }
  }, []);

  if (!canAccess) {
    return <div className="flex h-64 items-center justify-center"><p className="text-muted-foreground">{t("Permission required: super_admin")}</p></div>;
  }

  const active = SECTIONS.find((section) => section.key === activeSection) ?? SECTIONS[0];
  const Active = active.component;

  return (
    <div className="console-content-width min-h-[calc(100vh-64px)] pt-3">
      <Tabs
        type="card"
        collapsible
        activeKey={activeSection}
        onChange={setActiveSection}
        tabPaneMotion={false}
      >
        {SECTIONS.map((section) => (
          <TabPane
            key={section.key}
            itemKey={section.key}
            tab={<span className="flex items-center gap-1.5">{section.icon}{t(section.label)}</span>}
          >
            {activeSection === section.key ? (
              <Spin spinning={loading} size="large">
                <div className="min-h-40">
                  {!loading ? (
                    <Active key={active.key} options={options} loading={saving} onSave={handleSave} onBulkSave={handleBulkSave} />
                  ) : null}
                </div>
              </Spin>
            ) : null}
          </TabPane>
        ))}
      </Tabs>
    </div>
  );
}
