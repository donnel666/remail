import { useEffect, useState } from "react";
import { Input, Modal, Select, Switch, Toast } from "@douyinfe/semi-ui";
import { IconMail, IconUser, IconKey } from "@douyinfe/semi-icons";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createAdminUser,
  updateAdminUser,
  type AdminUser,
  type AdminUserRole,
} from "./admin-users-api";
import { useUserGroups } from "./use-user-groups";
import { roleLabel } from "./user-meta";

const ROLES: AdminUserRole[] = ["user", "supplier", "admin", "super_admin"];
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

interface CreateUserModalProps {
  canAssignSuperAdmin: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (user: AdminUser) => void | Promise<void>;
}

export function CreateUserModal({
  canAssignSuperAdmin,
  open,
  onOpenChange,
  onCreated,
}: CreateUserModalProps) {
  const { t } = useTranslation();
  const { groups } = useUserGroups();
  const [email, setEmail] = useState("");
  const [nickname, setNickname] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<AdminUserRole>("user");
  const [userGroupId, setUserGroupId] = useState<number>(0);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setEmail("");
    setNickname("");
    setPassword("");
    setRole("user");
  }, [open]);

  useEffect(() => {
    if (open) setUserGroupId(groups[0]?.id ?? 0);
  }, [open, groups]);

  const submit = async () => {
    if (role === "super_admin" && !canAssignSuperAdmin) return;
    const normalizedEmail = email.trim().toLowerCase();
    if (!normalizedEmail) {
      Toast.warning(t("Please enter your email."));
      return;
    }
    if (!EMAIL_PATTERN.test(normalizedEmail)) {
      Toast.warning(t("Please enter a valid email address."));
      return;
    }
    if (password.length < 6) {
      Toast.warning(t("Password must be at least 6 characters."));
      return;
    }
    setSaving(true);
    try {
      const user = await createAdminUser({
        email: normalizedEmail,
        nickname: nickname.trim() || undefined,
        password,
        role,
        userGroupId,
      });
      Toast.success(t("User created."));
      await onCreated(user);
      onOpenChange(false);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "User create failed."));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      centered
      confirmLoading={saving}
      onCancel={() => onOpenChange(false)}
      onOk={() => void submit()}
      okText={t("Create")}
      cancelText={t("Cancel")}
      style={{ width: 600 }}
      title={t("Create user")}
      visible={open}
    >
      <div className="space-y-4 py-1">
        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs text-[var(--semi-color-text-2)]">
          {t("Create managed user hint")}
        </div>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Email")} *
          </span>
          <Input
            autoFocus
            onChange={(value) => setEmail(String(value))}
            placeholder="user@example.com"
            prefix={<IconMail />}
            showClear
            value={email}
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Nickname")}
          </span>
          <Input
            maxLength={60}
            onChange={(value) => setNickname(String(value))}
            placeholder={t("Nickname optional")}
            prefix={<IconUser />}
            showClear
            value={nickname}
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Password")} *
          </span>
          <Input
            mode="password"
            onChange={(value) => setPassword(String(value))}
            placeholder={t("Password must be at least 6 characters.")}
            prefix={<IconKey />}
            value={password}
          />
          <div className="mt-1 text-xs text-[var(--semi-color-text-2)]">
            {t("The user can change this password after signing in.")}
          </div>
        </label>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Role")}
            </span>
            <Select
              onChange={(value) => setRole(String(value) as AdminUserRole)}
              style={{ width: "100%" }}
              value={role}
            >
              {ROLES.filter(
                (item) => item !== "super_admin" || canAssignSuperAdmin
              ).map((item) => (
                <Select.Option key={item} value={item}>
                  {roleLabel(item, t)}
                </Select.Option>
              ))}
            </Select>
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("User Group")}
            </span>
            <Select
              onChange={(value) => setUserGroupId(Number(value))}
              style={{ width: "100%" }}
              value={userGroupId}
            >
              {groups.map((group) => (
                <Select.Option key={group.id} value={group.id}>
                  {group.name}
                </Select.Option>
              ))}
            </Select>
          </label>
        </div>
      </div>
    </Modal>
  );
}

export function EditUserModal({
  user,
  canAssignSuperAdmin,
  onClose,
  onSaved,
}: {
  user: AdminUser | null;
  canAssignSuperAdmin: boolean;
  onClose: () => void;
  onSaved: (user: AdminUser) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const { groups } = useUserGroups();
  const [email, setEmail] = useState("");
  const [nickname, setNickname] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<AdminUserRole>("user");
  const [userGroupId, setUserGroupId] = useState(0);
  const [enabled, setEnabled] = useState(true);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!user) return;
    setEmail(user.email);
    setNickname(user.nickname);
    setPassword("");
    setRole(user.role);
    setUserGroupId(user.userGroup.id);
    setEnabled(user.enabled);
  }, [user]);

  const submit = async () => {
    if (!user) return;
    if (user.role === "super_admin") return;
    if (role === "super_admin" && !canAssignSuperAdmin) return;
    const normalizedEmail = email.trim().toLowerCase();
    if (!EMAIL_PATTERN.test(normalizedEmail)) {
      Toast.warning(t("Please enter a valid email address."));
      return;
    }
    if (password && password.length < 6) {
      Toast.warning(t("Password must be at least 6 characters."));
      return;
    }
    setSaving(true);
    try {
      const updated = await updateAdminUser(user.id, {
        email: normalizedEmail,
        nickname: nickname.trim(),
        password: password || undefined,
        role,
        userGroupId,
        enabled,
      });
      Toast.success(t("User updated."));
      await onSaved(updated);
      onClose();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "User update failed."));
    } finally {
      setSaving(false);
    }
  };

  const protectedUser = user?.role === "super_admin";

  return (
    <Modal
      centered
      confirmLoading={saving}
      onCancel={onClose}
      onOk={() => void submit()}
      okButtonProps={{ disabled: protectedUser }}
      okText={t("Save")}
      cancelText={t("Cancel")}
      style={{ width: 600 }}
      title={t("Edit user")}
      visible={Boolean(user)}
    >
      <div className="space-y-4 py-1">
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Email")} *
          </span>
          <Input
            disabled={protectedUser}
            onChange={(value) => setEmail(String(value))}
            placeholder="user@example.com"
            prefix={<IconMail />}
            showClear
            value={email}
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Nickname")}
          </span>
          <Input
            disabled={protectedUser}
            maxLength={60}
            onChange={(value) => setNickname(String(value))}
            value={nickname}
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("New password")}
          </span>
          <Input
            disabled={protectedUser}
            mode="password"
            onChange={(value) => setPassword(String(value))}
            placeholder={t("Leave blank to keep current")}
            prefix={<IconKey />}
            value={password}
          />
          <div className="mt-1 text-xs text-[var(--semi-color-text-2)]">
            {t("All sessions will be invalidated after password change.")}
          </div>
        </label>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Role")}
            </span>
            <Select
              disabled={protectedUser}
              onChange={(value) => setRole(String(value) as AdminUserRole)}
              style={{ width: "100%" }}
              value={role}
            >
              {ROLES.filter(
                (item) =>
                  item !== "super_admin" ||
                  canAssignSuperAdmin ||
                  user?.role === "super_admin"
              ).map((item) => (
                <Select.Option key={item} value={item}>
                  {roleLabel(item, t)}
                </Select.Option>
              ))}
            </Select>
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("User Group")}
            </span>
            <Select
              disabled={protectedUser}
              onChange={(value) => setUserGroupId(Number(value))}
              style={{ width: "100%" }}
              value={userGroupId}
            >
              {groups.map((group) => (
                <Select.Option key={group.id} value={group.id}>
                  {group.name}
                </Select.Option>
              ))}
            </Select>
          </label>
        </div>
        <div className="flex items-center justify-between rounded-lg border border-[var(--semi-color-border)] px-3 py-2">
          <div>
            <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Status")}
            </div>
            <div className="text-xs text-[var(--semi-color-text-2)]">
              {enabled ? t("Enabled") : t("Disabled")}
            </div>
          </div>
          <Switch
            checked={enabled}
            disabled={protectedUser}
            onChange={setEnabled}
            size="small"
          />
        </div>
      </div>
    </Modal>
  );
}
