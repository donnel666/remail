import { useEffect, useState } from "react";
import { Button, Modal, Toast } from "@douyinfe/semi-ui";
import { Edit, Plus, Users } from "lucide-react";
import { useTranslation } from "react-i18next";

import { createUserGroup, getUserGroups, updateUserGroup, type UserGroupFormValues } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { SettingsCardHeader, SettingsFormGrid, SettingsNumberField, SettingsSection, SettingsSwitchField, SettingsTextField } from "./settings-layout";

const EMPTY: UserGroupFormValues = { code: "", name: "", description: "", enabled: true, api_rpm_limit: 60, api_concurrency_limit: 3, api_quota_limit: 10000, price_discount_ratio: 1, topup_threshold: 0, auto_upgrade_enabled: false };

export default function UserGroupSection(_props: SectionProps) {
  const { t } = useTranslation();
  const [groups, setGroups] = useState<UserGroupFormValues[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState<UserGroupFormValues>({ ...EMPTY });
  const [showForm, setShowForm] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const result = await getUserGroups();
      setGroups(result.groups);
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : t("加载用户分组失败"));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const save = async () => {
    const code = editing.code.trim();
    const name = editing.name.trim();
    if (!code || !name) {
      Toast.warning(t("请填写分组标识码和分组名称"));
      return;
    }
    if (groups.some((group) => group.id !== editing.id && group.code.trim().toLowerCase() === code.toLowerCase())) {
      Toast.warning(t("分组标识码不能重复"));
      return;
    }
    setSaving(true);
    try {
      const { id, ...values } = { ...editing, code, name, description: editing.description.trim() };
      if (id) await updateUserGroup(id, values);
      else await createUserGroup(values);
      setShowForm(false);
      await load();
    } catch (error) {
      Toast.error(error instanceof Error ? error.message : t("保存用户分组失败"));
    } finally { setSaving(false); }
  };

  const number = (value: unknown) => Number(value) || 0;
  const update = (key: string, value: unknown) => setEditing((current) => ({ ...current, [key]: value }));

  return <div className="space-y-6">
    <SettingsSection title={<SettingsCardHeader icon={<Users size={16} />} title={t("用户分组管理")} description={t("管理用户分组、API 限制、价格折扣和自动升级规则")} />}>
      <div className="mt-4 flex items-center justify-between gap-4">
        <span className="text-sm text-[var(--semi-color-text-2)]">{t("当前分组")}：{groups.length}</span>
        <Button icon={<Plus size={14} />} theme="light" type="primary" onClick={() => { setEditing({ ...EMPTY }); setShowForm(true); }}>{t("创建分组")}</Button>
      </div>
      <div className="mt-3 overflow-hidden rounded-lg border border-[var(--semi-color-border)]">
        {groups.map((group) => <div key={group.id} className="flex items-center justify-between gap-4 border-b border-[var(--semi-color-border)] px-4 py-3 last:border-b-0 hover:bg-[var(--semi-color-fill-0)]">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">{group.name}</span>
              <code className="rounded bg-[var(--semi-color-fill-0)] px-1.5 py-0.5 text-xs">{group.code}</code>
              {!group.enabled ? <span className="rounded bg-red-50 px-1.5 py-0.5 text-xs text-red-600">{t("已禁用")}</span> : null}
            </div>
            <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-[var(--semi-color-text-2)]">
              <span>RPM: {group.api_rpm_limit}</span><span>{t("并发")}: {group.api_concurrency_limit}</span><span>{t("折扣")}: {group.price_discount_ratio}</span><span>{t("门槛")}: {group.topup_threshold}</span><span>{t("自动升级")}: {group.auto_upgrade_enabled ? t("是") : t("否")}</span>
            </div>
          </div>
          <Button icon={<Edit size={14} />} theme="light" type="tertiary" onClick={() => { setEditing({ ...group }); setShowForm(true); }}>{t("编辑")}</Button>
        </div>)}
        {groups.length === 0 && !loading ? <p className="px-6 py-10 text-center text-sm text-[var(--semi-color-text-2)]">{t("暂无分组")}</p> : null}
        {loading ? <p className="px-6 py-10 text-center text-sm text-[var(--semi-color-text-2)]">{t("加载中...")}</p> : null}
      </div>
      <p className="mt-3 text-xs text-[var(--semi-color-text-2)]">{t("管理员可随时手动调整用户分组，不受充值升级门槛限制。")}</p>
    </SettingsSection>

    <Modal
      cancelText={t("取消")}
      centered
      confirmLoading={saving}
      okText={t("保存设置")}
      onCancel={() => setShowForm(false)}
      onOk={() => void save()}
      title={editing.id ? t("编辑分组") : t("创建分组")}
      visible={showForm}
      width={800}
    >
      <div className="mb-4 text-sm text-[var(--semi-color-text-2)]">{t("配置分组基本信息、能力限制和升级规则")}</div>
      <SettingsFormGrid>
        <SettingsTextField label={t("分组标识码")} value={editing.code} onChange={(value) => update("code", value)} />
        <SettingsTextField label={t("分组名称")} value={editing.name} onChange={(value) => update("name", value)} />
        <SettingsTextField label={t("分组描述")} value={editing.description} onChange={(value) => update("description", value)} />
        <SettingsNumberField label={t("API 每分钟请求次数上限")} value={number(editing.api_rpm_limit)} onChange={(value) => update("api_rpm_limit", value)} min={0} />
        <SettingsNumberField label={t("API 并发请求数上限")} value={number(editing.api_concurrency_limit)} onChange={(value) => update("api_concurrency_limit", value)} min={0} />
        <SettingsNumberField label={t("API Key 总调用额度上限")} value={number(editing.api_quota_limit)} onChange={(value) => update("api_quota_limit", value)} min={0} />
        <SettingsNumberField label={t("价格折扣率（0.9 = 九折）")} value={number(editing.price_discount_ratio)} onChange={(value) => update("price_discount_ratio", value)} min={0} max={1} precision={2} step={0.01} />
        <SettingsNumberField label={t("充值升级门槛")} value={number(editing.topup_threshold)} onChange={(value) => update("topup_threshold", value)} min={0} />
        <SettingsSwitchField checked={editing.enabled} onChange={(value) => update("enabled", value)} label={t("启用分组")} />
        <SettingsSwitchField checked={editing.auto_upgrade_enabled} onChange={(value) => update("auto_upgrade_enabled", value)} label={t("允许自动升级")} description={t("达到充值门槛后自动升入此分组")} />
      </SettingsFormGrid>
    </Modal>
  </div>;
}
