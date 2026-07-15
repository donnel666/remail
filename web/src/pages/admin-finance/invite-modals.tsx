import { useEffect, useState } from "react";
import {
  DatePicker,
  Input,
  InputNumber,
  Modal,
  Switch,
  Toast,
} from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  batchCreateMockFinanceInvites,
  createMockFinanceInvite,
  updateMockFinanceInvite,
  type FinanceInvite,
} from "./admin-finance-mock";

interface CreateInviteModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (count: number) => void;
}

export function CreateInviteModal({
  open,
  onOpenChange,
  onCreated,
}: CreateInviteModalProps) {
  const { t } = useTranslation();
  const [code, setCode] = useState("");
  const [count, setCount] = useState<number | string>(1);
  const [maxUse, setMaxUse] = useState<number | string>(10);
  const [enabled, setEnabled] = useState(true);
  const [expireAt, setExpireAt] = useState<Date | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setCode("");
    setCount(1);
    setMaxUse(10);
    setEnabled(true);
    setExpireAt(null);
  }, [open]);

  const submit = async () => {
    const parsedCount = Number(count);
    const parsedMaxUse = Number(maxUse);
    if (!Number.isFinite(parsedCount) || parsedCount < 1) {
      Toast.warning(t("Count must be at least 1."));
      return;
    }
    if (parsedCount > 100) {
      Toast.warning(t("Cannot create more than 100 invite codes at once."));
      return;
    }
    if (!Number.isFinite(parsedMaxUse) || parsedMaxUse < 1) {
      Toast.warning(t("Max use must be at least 1."));
      return;
    }

    setSaving(true);
    try {
      const expireAtValue = expireAt ? expireAt.toISOString() : null;
      if (parsedCount === 1) {
        await createMockFinanceInvite({
          code: code.trim() || undefined,
          maxUse: parsedMaxUse,
          enabled,
          expireAt: expireAtValue,
        });
        Toast.success(t("Invite code created."));
        onCreated(1);
      } else {
        const result = await batchCreateMockFinanceInvites({
          count: parsedCount,
          maxUse: parsedMaxUse,
          enabled,
          expireAt: expireAtValue,
        });
        Toast.success(t("Invite codes created.", { count: result.created }));
        onCreated(result.created);
      }
      onOpenChange(false);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
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
      style={{ width: 560 }}
      title={t("Create invite code")}
      visible={open}
    >
      <div className="space-y-4 py-1">
        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs text-[var(--semi-color-text-2)]">
          {t("Create invite code hint")}
        </div>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Count")} *
            </span>
            <InputNumber
              max={100}
              min={1}
              onChange={setCount}
              style={{ width: "100%" }}
              value={count}
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Max uses")} *
            </span>
            <InputNumber
              min={1}
              onChange={setMaxUse}
              style={{ width: "100%" }}
              value={maxUse}
            />
          </label>
        </div>
        {Number(count) === 1 ? (
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Invite code")}
            </span>
            <Input
              maxLength={64}
              onChange={(value) => setCode(String(value).toUpperCase())}
              placeholder={t("Leave empty to auto-generate")}
              showClear
              value={code}
            />
          </label>
        ) : null}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Enabled")}
            </span>
            <div className="flex h-8 items-center">
              <Switch checked={enabled} onChange={setEnabled} />
            </div>
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Expire at")}
            </span>
            <DatePicker
              onChange={(value) =>
                setExpireAt(value instanceof Date ? value : null)
              }
              showClear
              style={{ width: "100%" }}
              type="dateTime"
              value={expireAt ?? undefined}
            />
          </label>
        </div>
      </div>
    </Modal>
  );
}

export function EditInviteModal({
  invite,
  onClose,
  onSaved,
}: {
  invite: FinanceInvite | null;
  onClose: () => void;
  onSaved: (invite: FinanceInvite) => void;
}) {
  const { t } = useTranslation();
  const [maxUse, setMaxUse] = useState<number | string>(1);
  const [expireAt, setExpireAt] = useState<Date | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!invite) return;
    setMaxUse(invite.maxUse >= 2147483647 ? 999999 : invite.maxUse);
    setExpireAt(invite.expireAt ? new Date(invite.expireAt) : null);
  }, [invite]);

  const submit = async () => {
    if (!invite) return;
    const parsedMaxUse = Number(maxUse);
    if (!Number.isFinite(parsedMaxUse) || parsedMaxUse < 1) {
      Toast.warning(t("Max use must be at least 1."));
      return;
    }
    setSaving(true);
    try {
      const updated = await updateMockFinanceInvite(invite.code, {
        maxUse: parsedMaxUse,
        expireAt: expireAt ? expireAt.toISOString() : null,
      });
      Toast.success(t("Invite code updated."));
      onSaved(updated);
      onClose();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Operation failed."));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      centered
      confirmLoading={saving}
      onCancel={onClose}
      onOk={() => void submit()}
      okText={t("Save")}
      cancelText={t("Cancel")}
      style={{ width: 520 }}
      title={t("Edit invite code")}
      visible={Boolean(invite)}
    >
      <div className="space-y-4 py-1">
        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2 text-sm">
          <span className="text-[var(--semi-color-text-2)]">{t("Invite code")}</span>
          <span className="ml-2 font-mono-data font-semibold">{invite?.code}</span>
        </div>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Max uses")} *
          </span>
          <InputNumber
            min={1}
            onChange={setMaxUse}
            style={{ width: "100%" }}
            value={maxUse}
          />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Expire at")}
          </span>
          <DatePicker
            onChange={(value) =>
              setExpireAt(value instanceof Date ? value : null)
            }
            showClear
            style={{ width: "100%" }}
            type="dateTime"
            value={expireAt ?? undefined}
          />
        </label>
      </div>
    </Modal>
  );
}
