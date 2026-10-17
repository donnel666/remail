import { useEffect, useMemo, useState } from "react";
import {
  Checkbox,
  Input,
  InputNumber,
  Modal,
  Select,
  Switch,
  Toast,
} from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import {
  AdminUserSelect,
  type AdminUserSelectOption,
} from "@/components/semi/admin-user-select";
import { hasPermission, useAuth } from "@/context/auth-provider";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { IamApiError } from "@/lib/api-client";
import {
  listAdminDaumOwners,
  getAdminDaumResourceDetail,
  replaceAdminDaumCredentials,
  updateAdminDaumResource,
} from "@/lib/admin-daum-api";

import { ImportDaumEmailsModal } from "../resources/import-daum-emails-modal";
import { InfoItem, ownerRoleLabel } from "./daum-meta";
import type {
  AdminDaumOwner,
  AdminDaumResourceDetail,
  AdminDaumResourceItem,
} from "./admin-daum-types";

function ownerOption(
  owner: AdminDaumOwner,
  t: ReturnType<typeof useTranslation>["t"]
): AdminUserSelectOption<AdminDaumOwner> {
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
  owners: AdminDaumOwner[];
  selectedOwner?: AdminDaumOwner;
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
        (await listAdminDaumOwners(keyword)).map((owner) =>
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

export function ImportDaumModal({ onCancel, onImported, owners, visible }: {
  onCancel: () => void; onImported: () => void | Promise<void>; owners: AdminDaumOwner[]; visible: boolean;
}) {
  return <ImportDaumEmailsModal open={visible} owners={owners} onOpenChange={(open) => { if (!open) onCancel(); }} onSuccess={onImported} />;
}

export function EditDaumModal({ onCancel, onSaved, owners, target }: {
  onCancel: () => void; onSaved: () => void | Promise<void>; owners: AdminDaumOwner[]; target: AdminDaumResourceItem | null;
}) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const canWrite = hasPermission(currentUser, "core:resource", "write");
  const canOperate = hasPermission(currentUser, "core:resource", "operate");
  const canEditBinding = hasPermission(currentUser, "mailtransport:binding", "write");
  const [forSale, setForSale] = useState(false);
  const [password, setPassword] = useState("");
  const [appPassword, setAppPassword] = useState("");
  const [clearBinding, setClearBinding] = useState(false);
  const [emailAddress, setEmailAddress] = useState("");
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [longLived, setLongLived] = useState(false);
  const [protocol, setProtocol] = useState<"imap" | "pop3">("imap");
  const [bindingAddress, setBindingAddress] = useState("");
  const [kakaoEmail, setKakaoEmail] = useState("");
  const [pickupUrl, setPickupUrl] = useState("");
  const [qualityScore, setQualityScore] = useState<number | string>("");
  const [submitting, setSubmitting] = useState(false);
  useEffect(() => {
    setPassword("");
    setAppPassword("");
    setClearBinding(false);
    if (!target) return;
    setForSale(target.forSale);
    setEmailAddress(target.emailAddress);
    setOwnerId(target.owner?.id ?? target.ownerUserId);
    setLongLived(target.longLived);
    setQualityScore(target.qualityScore);
    setProtocol(target.protocol);
    setBindingAddress(target.bindingAddress ?? "");
    setKakaoEmail(target.kakaoEmail ?? "");
    setPickupUrl("");
  }, [target]);
  const submit = async () => {
    if (!target || !ownerId || submitting || !canWrite) return;
    if (!/^[^\s@]+@[^\s@]+$/.test(emailAddress.trim())) {
      Toast.warning(t("A valid Daum email address is required."));
      return;
    }
    setSubmitting(true);
    try {
      await updateAdminDaumResource(target.id, {
        version: target.version,
        longLived,
        ...(emailAddress.trim().toLowerCase() !== target.emailAddress.toLowerCase() ? { email: emailAddress.trim() } : {}),
        ...(forSale !== target.forSale ? { forSale } : {}),
        ...(password ? { password } : {}),
        ...(appPassword ? { appPassword } : {}),
        ...(ownerId !== target.ownerUserId ? { ownerId } : {}),
        ...(protocol !== target.protocol ? { protocol } : {}),
        ...(kakaoEmail.trim() !== (target.kakaoEmail ?? "") ? { kakaoEmail: kakaoEmail.trim() } : {}),
        ...(clearBinding ? { bindingAddress: null, pickupUrl: null } : {
          ...(!target.pickupUrlConfigured && !pickupUrl.trim() && bindingAddress.trim() !== (target.bindingAddress ?? "") ? { bindingAddress: bindingAddress.trim() || null } : {}),
          ...(pickupUrl.trim() ? { pickupUrl: pickupUrl.trim() } : {}),
        }),
        qualityScore: qualityScore === "" || !Number.isFinite(Number(qualityScore)) ? undefined : Number(qualityScore),
      });
      Toast.success(t("Daum resource updated."));
      try { await onSaved(); }
      catch (error) { Toast.error(getIamErrorMessage(t, error, "Admin Daum resources load failed.")); }
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Daum resource update failed."));
      if (error instanceof IamApiError && error.code === "resource_version_conflict") { await onSaved(); onCancel(); }
    } finally { setSubmitting(false); }
  };
  return <Modal cancelText={t("Cancel")} centered confirmLoading={submitting}
    onCancel={() => { if (!submitting) onCancel(); }} onOk={() => void submit()} okText={t("Save")}
    okButtonProps={{ disabled: !canWrite }} title={t("Edit Daum resource")} visible={Boolean(target)} width={580}>
    {target ? <div className="space-y-4 py-1">
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Email")} *</span>
          <Input className="font-mono" disabled={!canWrite} onChange={setEmailAddress} placeholder="name@daum.net" value={emailAddress} />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Auxiliary pickup email")}</span>
          <Input className="font-mono" autoComplete="off" disabled={!canEditBinding || clearBinding}
            readOnly={target.pickupUrlConfigured || Boolean(pickupUrl.trim())}
            onChange={setBindingAddress} placeholder={t("Optional recovery mailbox")} showClear={!target.pickupUrlConfigured && !pickupUrl.trim()} value={bindingAddress} />
        </label>
      </div>
      <div className="text-xs text-[var(--semi-color-text-2)]">{t("Email is the resource identity; edit it only to correct a mistake.")}</div>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Kakao account email")}</span>
          <Input className="font-mono" autoComplete="off" onChange={setKakaoEmail} value={kakaoEmail} />
        </label>
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("remail pickup URL")}</span>
          <Input autoComplete="off" disabled={!canEditBinding || clearBinding} mode="password" onChange={setPickupUrl} value={pickupUrl} placeholder={t("Leave blank to keep the existing pickup URL")} />
        </label>
      </div>
      {target.bindingAddress || target.pickupUrlConfigured ? <Checkbox checked={clearBinding} disabled={!canEditBinding} onChange={(event) => setClearBinding(Boolean(event.target.checked))}>{t("Unlink auxiliary mailbox")}</Checkbox> : null}
      <label className="block">
        <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Owner")}</span>
        <OwnerSelect onChange={setOwnerId} owners={owners} selectedOwner={target.owner ?? undefined} t={t} value={ownerId} />
      </label>
      <label className="block">
        <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Quality score")}</span>
        <InputNumber max={100} min={0} onChange={setQualityScore} precision={0} step={1} style={{ width: "100%" }} value={qualityScore} />
      </label>
      <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5">
        <div><div className="text-sm font-medium text-[var(--semi-color-text-0)]">{t("Public sale")}</div><div className="text-xs text-[var(--semi-color-text-2)]">{t("Public-sale resources require an enabled supplier or administrator owner.")}</div></div>
        <Switch aria-label={t("Public sale")} checked={forSale} disabled={!canOperate} onChange={setForSale} size="small" />
      </div>
      <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5">
        <div>
          <div className="text-sm font-medium text-[var(--semi-color-text-0)]">{t("Long-lived")}</div>
          <div className="text-xs text-[var(--semi-color-text-2)]">{t("Lifetime is an administrator-managed resource classification.")}</div>
        </div>
        <Switch checked={longLived} onChange={setLongLived} size="small" />
      </div>
      <div className="rounded-lg border border-[var(--semi-color-border)] p-3">
        <div className="mb-1 text-sm font-medium text-[var(--semi-color-text-0)]">{t("Credentials")}</div>
        <div className="mb-3 text-xs text-[var(--semi-color-text-2)]">{t("Write-only. Leave blank to keep current")}</div>
        <div className="space-y-3">
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Login password")}</span>
            <Input autoComplete="new-password" mode="password" disabled={!canOperate} onChange={setPassword} value={password} placeholder={t("Leave blank to keep the existing login password")} />
          </label>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("APP password")}</span>
              <Input autoComplete="new-password" mode="password" disabled={!canOperate} onChange={setAppPassword} value={appPassword} placeholder={t("Leave blank to keep current")} />
            </label>
            <label className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Mail protocol")}</span>
              <Select disabled={!canOperate} aria-label={t("Mail protocol")} style={{ width: "100%" }} value={protocol} onChange={(value) => setProtocol(value === "pop3" ? "pop3" : "imap")}>
                <Select.Option value="imap">IMAP</Select.Option>
                <Select.Option value="pop3">POP3</Select.Option>
              </Select>
            </label>
          </div>
        </div>
      </div>
    </div> : null}
  </Modal>;
}

export function ReplaceCredentialsModal({ onCancel, onSaved, target }: {
  onCancel: () => void; onSaved: (detail: AdminDaumResourceDetail) => void | Promise<void>; target: AdminDaumResourceItem | null;
}) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [appPassword, setAppPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const clearCredentials = () => { setPassword(""); setAppPassword(""); };
  useEffect(() => { clearCredentials(); }, [target]);
  const submit = async () => {
    if (!target || submitting) return;
    if (!appPassword) { Toast.warning(t("Daum APP password is required.")); return; }
    setSubmitting(true);
    try {
      const detail = await replaceAdminDaumCredentials(target.id, { version: target.version, appPassword, ...(password ? { password } : {}) });
      Toast.success(t("Credentials replaced and validation queued."));
      try { await onSaved(detail); }
      catch (error) { Toast.error(getIamErrorMessage(t, error, "Admin Daum resources load failed.")); }
      clearCredentials();
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Credential replacement failed."));
      if (error instanceof IamApiError && error.code === "resource_version_conflict") { await onSaved(await getAdminDaumResourceDetail(target.id)); clearCredentials(); onCancel(); }
    } finally { setSubmitting(false); }
  };
  return <Modal cancelText={t("Cancel")} centered confirmLoading={submitting}
    onCancel={() => { if (!submitting) { clearCredentials(); onCancel(); } }} onOk={() => void submit()}
    okText={t("Replace credentials")} size="small" title={t("Replace Daum credentials")} visible={Boolean(target)}>
    {target ? <div className="space-y-4 py-1">
      <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
        {t("Daum credentials are write-only. Enter a new APP password; leave the login password blank to keep its existing value.")}
      </div>
      <InfoItem label={t("Email")} value={<span className="font-mono">{target.emailAddress}</span>} />
      <label className="block">
        <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("APP password")} *</span>
        <Input autoComplete="new-password" mode="password" onChange={setAppPassword} placeholder={t("Enter a replacement APP password")} value={appPassword} />
      </label>
      <label className="block">
        <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Login password")}</span>
        <Input autoComplete="new-password" mode="password" onChange={setPassword}
          placeholder={t("Leave blank to keep the existing login password")} value={password} />
      </label>
    </div> : null}
  </Modal>;
}
