import { useEffect, useMemo, useState } from "react";
import {
  Input,
  InputNumber,
  Modal,
  Switch,
  Toast,
} from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import {
  AdminUserSelect,
  type AdminUserSelectOption,
} from "@/components/semi/admin-user-select";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { IamApiError } from "@/lib/api-client";
import {
  listAdminProtoOwners,
  getAdminProtoResourceDetail,
  replaceAdminProtoCredentials,
  updateAdminProtoResource,
} from "@/lib/admin-proto-api";

import { ImportProtoEmailsModal } from "../resources/import-proto-emails-modal";
import { InfoItem, ownerRoleLabel } from "./proto-meta";
import type {
  AdminProtoOwner,
  AdminProtoResourceDetail,
  AdminProtoResourceItem,
} from "./admin-proto-types";

function ownerOption(
  owner: AdminProtoOwner,
  t: ReturnType<typeof useTranslation>["t"]
): AdminUserSelectOption<AdminProtoOwner> {
  return {
    data: owner,
    disabled: !owner.enabled,
    label: `${owner.email} · ${owner.nickname} · ${t(ownerRoleLabel(owner.role))} · ${owner.groupName}`,
    value: owner.id,
  };
}

function OwnerSelect({
  onChange,
  owners,
  selectedOwner,
  t,
  value,
}: {
  onChange: (ownerId: number) => void;
  owners: AdminProtoOwner[];
  selectedOwner?: AdminProtoOwner;
  t: ReturnType<typeof useTranslation>["t"];
  value?: number;
}) {
  const options = useMemo(
    () => owners.map((owner) => ownerOption(owner, t)),
    [owners, t]
  );
  const selectedOption = useMemo(
    () => (selectedOwner ? ownerOption(selectedOwner, t) : undefined),
    [selectedOwner, t]
  );

  return (
    <AdminUserSelect
      emptyContent={t("No users found")}
      loadOptions={async (keyword) =>
        (await listAdminProtoOwners(keyword)).map((owner) =>
          ownerOption(owner, t)
        )
      }
      onChange={(ownerID) => {
        if (ownerID) onChange(ownerID);
      }}
      options={options}
      placeholder={t("Search user by email, nickname or ID")}
      selectedOption={selectedOption}
      style={{ width: "100%" }}
      value={value}
    />
  );
}

export function ImportProtoModal({ onCancel, onImported, owners, visible }: {
  onCancel: () => void; onImported: () => void | Promise<void>; owners: AdminProtoOwner[]; visible: boolean;
}) {
  return <ImportProtoEmailsModal admin open={visible} owners={owners} onOpenChange={(open) => { if (!open) onCancel(); }} onSuccess={onImported} />;
}

export function EditProtoModal({ onCancel, onSaved, owners, target }: {
  onCancel: () => void; onSaved: () => void | Promise<void>; owners: AdminProtoOwner[]; target: AdminProtoResourceItem | null;
}) {
  const { t } = useTranslation();
  const [emailAddress, setEmailAddress] = useState("");
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [longLived, setLongLived] = useState(false);
  const [qualityScore, setQualityScore] = useState<number | string>("");
  const [submitting, setSubmitting] = useState(false);
  useEffect(() => {
    if (!target) return;
    setEmailAddress(target.emailAddress);
    setOwnerId(target.owner?.id ?? target.ownerUserId);
    setLongLived(target.longLived);
    setQualityScore(target.qualityScore);
  }, [target]);
  const submit = async () => {
    if (!target || !ownerId || submitting) return;
    if (!/^[^\s@]+@[^\s@]+$/.test(emailAddress.trim())) {
      Toast.warning(t("A valid Proto email address is required."));
      return;
    }
    setSubmitting(true);
    try {
      await updateAdminProtoResource(target.id, {
        version: target.version,
        email: emailAddress.trim(),
        ownerId, longLived,
        qualityScore: qualityScore === "" || !Number.isFinite(Number(qualityScore)) ? undefined : Number(qualityScore),
      });
      Toast.success(t("Proto resource updated."));
      try { await onSaved(); }
      catch (error) { Toast.error(getIamErrorMessage(t, error, "Admin Proto resources load failed.")); }
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Proto resource update failed."));
      if (error instanceof IamApiError && error.code === "resource_version_conflict") { await onSaved(); onCancel(); }
    } finally { setSubmitting(false); }
  };
  return <Modal cancelText={t("Cancel")} centered confirmLoading={submitting}
    onCancel={() => { if (!submitting) onCancel(); }} onOk={() => void submit()} okText={t("Save")}
    title={t("Edit Proto resource")} visible={Boolean(target)} width={580}>
    {target ? <div className="space-y-4 py-1">
      <label className="block">
        <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Email")} *</span>
        <Input className="font-mono" onChange={setEmailAddress} value={emailAddress} />
      </label>
      <div className="text-xs text-[var(--semi-color-text-2)]">{t("Changing the email clears credentials and requires a replacement password.")}</div>
      <label className="block">
        <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Owner")}</span>
        <OwnerSelect onChange={setOwnerId} owners={owners} selectedOwner={target.owner ?? undefined} t={t} value={ownerId} />
      </label>
      <label className="block">
        <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Quality score")}</span>
        <InputNumber max={100} min={0} onChange={setQualityScore} precision={0} step={1} style={{ width: "100%" }} value={qualityScore} />
      </label>
      <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5">
        <div>
          <div className="text-sm font-medium text-[var(--semi-color-text-0)]">{t("Long-lived")}</div>
          <div className="text-xs text-[var(--semi-color-text-2)]">{t("Lifetime is an administrator-managed resource classification.")}</div>
        </div>
        <Switch checked={longLived} onChange={setLongLived} size="small" />
      </div>
    </div> : null}
  </Modal>;
}

export function ReplaceCredentialsModal({ onCancel, onSaved, target }: {
  onCancel: () => void; onSaved: (detail: AdminProtoResourceDetail) => void | Promise<void>; target: AdminProtoResourceItem | null;
}) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  useEffect(() => setPassword(""), [target]);
  const submit = async () => {
    if (!target || submitting) return;
    if (!password) { Toast.warning(t("Proto account password is required.")); return; }
    setSubmitting(true);
    try {
      const detail = await replaceAdminProtoCredentials(target.id, { version: target.version, password });
      Toast.success(t("Credentials replaced and validation queued."));
      try { await onSaved(detail); }
      catch (error) { Toast.error(getIamErrorMessage(t, error, "Admin Proto resources load failed.")); }
      setPassword("");
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Credential replacement failed."));
      if (error instanceof IamApiError && error.code === "resource_version_conflict") { await onSaved(await getAdminProtoResourceDetail(target.id)); setPassword(""); onCancel(); }
    } finally { setSubmitting(false); }
  };
  return <Modal cancelText={t("Cancel")} centered confirmLoading={submitting}
    onCancel={() => { if (!submitting) { setPassword(""); onCancel(); } }} onOk={() => void submit()}
    okText={t("Replace credentials")} size="small" title={t("Replace Proto credentials")} visible={Boolean(target)}>
    {target ? <div className="space-y-4 py-1">
      <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
        {t("All credential fields are write-only. Existing values are never displayed, and submitting replaces the complete credential set.")}
      </div>
      <InfoItem label={t("Email")} value={<span className="font-mono">{target.emailAddress}</span>} />
      <label className="block">
        <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Password")} *</span>
        <Input autoComplete="new-password" mode="password" onChange={setPassword}
          placeholder={t("Enter a replacement password")} value={password} />
      </label>
    </div> : null}
  </Modal>;
}
