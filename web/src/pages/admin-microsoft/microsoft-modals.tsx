import { useEffect, useMemo, useRef, useState } from "react";
import {
  Input,
  InputNumber,
  Modal,
  Switch,
  TextArea,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { FileText, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  AdminUserSelect,
  type AdminUserSelectOption,
} from "@/components/semi/admin-user-select";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  importAdminMicrosoftResources,
  listAdminMicrosoftOwners,
  replaceAdminMicrosoftCredentials,
  updateAdminMicrosoftResource,
  waitForAdminMicrosoftResourceImport,
} from "@/lib/admin-microsoft-api";

import { MICROSOFT_EMAIL_FORMAT_HINT } from "../resources/model";
import { InfoItem, ownerRoleLabel } from "./microsoft-meta";
import type {
  AdminMicrosoftImportErrorStrategy,
  AdminMicrosoftOwner,
  AdminMicrosoftResourceDetail,
  AdminMicrosoftResourceItem,
} from "./admin-microsoft-types";

const { Text } = Typography;

// Segmented toggle-card styling, matching the console import modal
// (resources/import-microsoft-emails-modal.tsx) for a consistent look.
const IMPORT_ENTRY_AREA_HEIGHT = 208;
// ponytail: mirror the server hard cap; lower configured limits stay server-enforced.
const MAX_IMPORT_FILE_BYTES = 512 * 1024 * 1024;

function switchButtonClass(active: boolean) {
  return [
    "flex h-12 w-full items-center justify-center gap-2 rounded-lg border-2 px-4 text-sm font-semibold transition-all",
    active
      ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
      : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] text-[var(--semi-color-text-1)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]",
  ].join(" ");
}

function ownerOption(
  owner: AdminMicrosoftOwner,
  t: ReturnType<typeof useTranslation>["t"]
): AdminUserSelectOption<AdminMicrosoftOwner> {
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
  owners: AdminMicrosoftOwner[];
  selectedOwner?: AdminMicrosoftOwner;
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
        (await listAdminMicrosoftOwners(keyword)).map((owner) =>
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

export function ImportMicrosoftModal({
  onCancel,
  onImported,
  owners,
  visible,
}: {
  onCancel: () => void;
  onImported: () => void | Promise<void>;
  owners: AdminMicrosoftOwner[];
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<"paste" | "file">("paste");
  const [content, setContent] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [longLived, setLongLived] = useState(true);
  const [errorStrategy, setErrorStrategy] =
    useState<AdminMicrosoftImportErrorStrategy>("skip");
  const [submitting, setSubmitting] = useState(false);
  const [polling, setPolling] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const importAbortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!visible) return;
    setMode("paste");
    setContent("");
    setFile(null);
    setOwnerId(undefined);
    setLongLived(true);
    setErrorStrategy("skip");
    setSubmitting(false);
    setPolling(false);
    return () => {
      importAbortRef.current?.abort();
      importAbortRef.current = null;
    };
  }, [visible]);

  useEffect(() => {
    if (!visible || ownerId !== undefined) return;
    setOwnerId(owners.find((owner) => owner.enabled)?.id ?? owners[0]?.id);
  }, [ownerId, owners, visible]);

  const lines = useMemo(
    () => content.split(/\r?\n/).filter((line) => line.trim().length > 0),
    [content]
  );

  const selectFile = (selected: File | null) => {
    if (fileRef.current) fileRef.current.value = "";
    if (submitting || importAbortRef.current) return;
    if (selected && !selected.name.toLowerCase().endsWith(".txt")) {
      setFile(null);
      Toast.warning(t("Please select a TXT file."));
      return;
    }
    if (selected && selected.size > MAX_IMPORT_FILE_BYTES) {
      setFile(null);
      Toast.warning(t("Import file must not exceed {{max}} MiB.", {
        max: MAX_IMPORT_FILE_BYTES / 1024 / 1024,
      }));
      return;
    }
    setFile(selected);
  };

  const close = () => {
    if (submitting && !polling) return;
    if (polling) Toast.info(t("Resource import continues in background."));
    importAbortRef.current?.abort();
    importAbortRef.current = null;
    setContent("");
    setFile(null);
    setSubmitting(false);
    setPolling(false);
    onCancel();
  };

  const submit = async () => {
    if (submitting || importAbortRef.current) return;
    if (!ownerId) {
      Toast.warning(t("Please select an owner."));
      return;
    }
    if (mode === "paste" ? lines.length === 0 : !file) {
      Toast.warning(
        t(mode === "file" ? "Please select a TXT file." : "Please enter Microsoft resources.")
      );
      return;
    }
    const controller = new AbortController();
    importAbortRef.current = controller;
    const isCurrentImport = () =>
      importAbortRef.current === controller && !controller.signal.aborted;
    setSubmitting(true);
    try {
      let sourceContent = content;
      if (mode === "file" && file) {
        const bytes = await file.arrayBuffer();
        if (!isCurrentImport()) return;
        try {
          sourceContent = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
        } catch {
          Toast.error(t("Import file must be UTF-8 encoded."));
          return;
        }
      }
      if (!sourceContent.trim()) {
        Toast.warning(t("Please enter Microsoft resources."));
        return;
      }
      const accepted = await importAdminMicrosoftResources({
        content: sourceContent,
        errorStrategy,
        longLived,
        ownerId,
      }, controller.signal);
      if (!isCurrentImport()) return;
      let response = accepted;
      if (accepted.status === "processing") {
        setPolling(true);
        Toast.success(t("Resource import accepted."));
        response = await waitForAdminMicrosoftResourceImport(accepted.importId, {
          signal: controller.signal,
        });
        if (!isCurrentImport()) return;
      }
      if (response.status === "failed") {
        throw new Error(response.lastSafeError || "Resource import failed.");
      }
      Toast.success(
        t("Microsoft resources imported.", {
          count: response.imported,
        })
      );
      if (response.skipped > 0) {
        Toast.warning(t("Import skipped errors", { count: response.skipped }));
      }
      try {
        await onImported();
      } catch (error) {
        if (isCurrentImport()) {
          Toast.error(getIamErrorMessage(t, error, "Admin Microsoft resources load failed."));
        }
      }
      if (isCurrentImport()) {
        setContent("");
        setFile(null);
        onCancel();
      }
    } catch (error) {
      if (isCurrentImport()) {
        Toast.error(getIamErrorMessage(t, error, "Resource import failed."));
      }
    } finally {
      if (isCurrentImport()) {
        importAbortRef.current = null;
        setSubmitting(false);
        setPolling(false);
      }
    }
  };

  return (
    <Modal
      cancelButtonProps={{ disabled: submitting && !polling }}
      cancelText={polling ? t("Continue in background") : t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={close}
      onOk={() => void submit()}
      okButtonProps={{ disabled: mode === "paste" ? lines.length === 0 : !file }}
      okText={t("Import")}
      title={t("Import Microsoft Emails")}
      visible={visible}
      width="min(666px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Owner")} *
          </span>
          <OwnerSelect onChange={setOwnerId} owners={owners} t={t} value={ownerId} />
        </label>

        <div className="grid grid-cols-2 gap-2">
          <button
            aria-pressed={mode === "paste"}
            className={switchButtonClass(mode === "paste")}
            disabled={submitting}
            onClick={() => {
              setMode("paste");
              setFile(null);
            }}
            type="button"
          >
            <FileText size={16} />
            {t("Manual input")}
          </button>
          <button
            aria-pressed={mode === "file"}
            className={switchButtonClass(mode === "file")}
            disabled={submitting}
            onClick={() => {
              setMode("file");
              setContent("");
            }}
            type="button"
          >
            <Upload size={16} />
            {t("TXT file")}
          </button>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(longLived)}
            onClick={() => setLongLived(true)}
            type="button"
          >
            {t("Long-lived")}
          </button>
          <button
            className={switchButtonClass(!longLived)}
            onClick={() => setLongLived(false)}
            type="button"
          >
            {t("Short-lived")}
          </button>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(errorStrategy === "skip")}
            onClick={() => setErrorStrategy("skip")}
            type="button"
          >
            {t("Skip errors")}
          </button>
          <button
            className={switchButtonClass(errorStrategy === "abort")}
            onClick={() => setErrorStrategy("abort")}
            type="button"
          >
            {t("Abort on error")}
          </button>
        </div>

        {mode === "paste" ? (
          <label className="block">
            <span className="mb-1.5 flex items-center justify-between text-sm font-medium text-[var(--semi-color-text-0)]">
              <span>{t("Microsoft resource entries")} *</span>
              <Text size="small" type="tertiary">
                {t("Parsed entries", { count: lines.length })}
              </Text>
            </span>
            <TextArea
              className="font-mono"
              onChange={(value) => setContent(value)}
              placeholder="email----password"
              rows={8}
              style={{ height: IMPORT_ENTRY_AREA_HEIGHT, resize: "none" }}
              value={content}
            />
          </label>
        ) : (
          <div>
            <input
              accept=".txt"
              aria-label={t("TXT file")}
              className="hidden"
              disabled={submitting}
              onChange={(event) => selectFile(event.target.files?.[0] ?? null)}
              ref={fileRef}
              type="file"
            />
            <button
              className="flex w-full flex-col items-center justify-center rounded-xl border border-dashed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-6 text-center transition-colors hover:bg-[var(--semi-color-fill-1)]"
              disabled={submitting}
              onClick={() => fileRef.current?.click()}
              onDragOver={(event) => event.preventDefault()}
              onDrop={(event) => {
                event.preventDefault();
                selectFile(event.dataTransfer.files[0] ?? null);
              }}
              style={{ height: IMPORT_ENTRY_AREA_HEIGHT }}
              type="button"
            >
              <FileText className="mb-2 size-8 text-[var(--semi-color-text-2)]" />
              <Text strong>
                {file ? file.name : t("Click to select or drag file here")}
              </Text>
              <Text size="small" type="tertiary">
                {file
                  ? `${(file.size / 1024).toFixed(1)} KB`
                  : t("Supports .txt files, one entry per line")}
              </Text>
            </button>
          </div>
        )}

        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
            {t("Supported format")}
          </div>
          <pre className="font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            {MICROSOFT_EMAIL_FORMAT_HINT}
          </pre>
        </div>

        <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
          {t(
            "Credentials are accepted as write-only input. Passwords, client IDs and tokens are never returned by this page."
          )}
        </div>
      </div>
    </Modal>
  );
}

export function EditMicrosoftModal({
  onCancel,
  onSaved,
  owners,
  target,
}: {
  onCancel: () => void;
  onSaved: () => void | Promise<void>;
  owners: AdminMicrosoftOwner[];
  target: AdminMicrosoftResourceItem | null;
}) {
  const { t } = useTranslation();
  const [emailAddress, setEmailAddress] = useState("");
  const [bindingAddress, setBindingAddress] = useState("");
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [forSale, setForSale] = useState(false);
  const [longLived, setLongLived] = useState(false);
  const [qualityScore, setQualityScore] = useState<number | string>("");
  const [password, setPassword] = useState("");
  const [clientId, setClientId] = useState("");
  const [refreshToken, setRefreshToken] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!target) return;
    setEmailAddress(target.emailAddress);
    setBindingAddress(target.bindingAddress ?? "");
    setOwnerId(target.owner.id);
    setForSale(target.forSale);
    setLongLived(target.longLived);
    setQualityScore(target.qualityScore);
    setPassword("");
    setClientId("");
    setRefreshToken("");
  }, [target]);

  const submit = async () => {
    if (!target || !ownerId) return;
    if (!emailAddress.trim()) {
      Toast.warning(t("A valid Microsoft email address is required."));
      return;
    }
    const wantsCredentialChange = Boolean(
      password || clientId.trim() || refreshToken.trim()
    );
    if (wantsCredentialChange) {
      if (!password) {
        Toast.warning(t("Microsoft account password is required."));
        return;
      }
      if (Boolean(clientId.trim()) !== Boolean(refreshToken.trim())) {
        Toast.warning(t("OAuth client ID and refresh token must be configured together."));
        return;
      }
    }
    setSubmitting(true);
    const nextBindingAddress = bindingAddress.trim() || null;
    const currentBindingAddress = target.bindingAddress?.trim() || null;
    try {
      await updateAdminMicrosoftResource(target.id, {
        ...(nextBindingAddress !== currentBindingAddress
          ? { bindingAddress: nextBindingAddress }
          : {}),
        credentials: wantsCredentialChange
          ? {
              clientId: clientId.trim() || undefined,
              password,
              refreshToken: refreshToken.trim() || undefined,
            }
          : undefined,
        emailAddress: emailAddress.trim(),
        forSale,
        longLived,
        ownerId,
        qualityScore:
          qualityScore === "" || !Number.isFinite(Number(qualityScore))
            ? undefined
            : Number(qualityScore),
        version: target.version,
      });
      Toast.success(t("Microsoft resource updated."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Microsoft resource update failed."));
      setSubmitting(false);
      return;
    }
    try {
      await onSaved();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(t, error, "Admin Microsoft resources load failed.")
      );
    } finally {
      setSubmitting(false);
    }
    onCancel();
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => void submit()}
      okText={t("Save")}
      title={t("Edit Microsoft resource")}
      visible={Boolean(target)}
      width={580}
    >
      {target ? (
        <div className="space-y-4 py-1">
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Email")} *
              </span>
              <Input
                className="font-mono"
                onChange={(value) => setEmailAddress(String(value))}
                placeholder="name@outlook.com"
                value={emailAddress}
              />
            </label>
            <label className="block">
              <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Auxiliary email")}
              </span>
              <Input
                className="font-mono"
                onChange={(value) => setBindingAddress(String(value))}
                placeholder={t("Optional recovery mailbox")}
                showClear
                value={bindingAddress}
              />
            </label>
          </div>
          <div className="text-xs text-[var(--semi-color-text-2)]">
            {t(
              "Email is the resource identity; edit it only to correct a mistake."
            )}
          </div>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Owner")}
            </span>
            <OwnerSelect
              onChange={setOwnerId}
              owners={owners}
              selectedOwner={target.owner}
              t={t}
              value={ownerId}
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Quality score")}
            </span>
            <InputNumber
              max={100}
              min={0}
              onChange={setQualityScore}
              precision={0}
              step={1}
              style={{ width: "100%" }}
              value={qualityScore}
            />
          </label>
          <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5">
            <div>
              <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Public sale")}
              </div>
              <div className="text-xs text-[var(--semi-color-text-2)]">
                {t("Public-sale resources require an enabled supplier or administrator owner.")}
              </div>
            </div>
            <Switch checked={forSale} onChange={setForSale} size="small" />
          </div>
          <div className="flex items-center justify-between rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2.5">
            <div>
              <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Long-lived")}
              </div>
              <div className="text-xs text-[var(--semi-color-text-2)]">
                {t("Lifetime is an administrator-managed resource classification.")}
              </div>
            </div>
            <Switch checked={longLived} onChange={setLongLived} size="small" />
          </div>

          <div className="rounded-lg border border-[var(--semi-color-border)] p-3">
            <div className="mb-1 text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Credentials")}
            </div>
            <div className="mb-3 text-xs text-[var(--semi-color-text-2)]">
              {t(
                "Write-only. Leave blank to keep the current values; filling password replaces the whole credential set and re-queues validation."
              )}
            </div>
            <div className="space-y-3">
              <label className="block">
                <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                  {t("Password")}
                </span>
                <Input
                  autoComplete="new-password"
                  mode="password"
                  onChange={(value) => setPassword(String(value))}
                  placeholder={t("Leave blank to keep current")}
                  value={password}
                />
              </label>
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("OAuth client ID")}
                  </span>
                  <Input
                    autoComplete="off"
                    mode="password"
                    onChange={(value) => setClientId(String(value))}
                    placeholder={t("Leave blank to keep current")}
                    value={clientId}
                  />
                </label>
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Refresh token")}
                  </span>
                  <Input
                    autoComplete="off"
                    mode="password"
                    onChange={(value) => setRefreshToken(String(value))}
                    placeholder={t("Leave blank to keep current")}
                    value={refreshToken}
                  />
                </label>
              </div>
            </div>
          </div>
        </div>
      ) : null}
    </Modal>
  );
}

export function ReplaceCredentialsModal({
  onCancel,
  onSaved,
  target,
}: {
  onCancel: () => void;
  onSaved: (detail: AdminMicrosoftResourceDetail) => void | Promise<void>;
  target: AdminMicrosoftResourceItem | null;
}) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [clientId, setClientId] = useState("");
  const [refreshToken, setRefreshToken] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    setPassword("");
    setClientId("");
    setRefreshToken("");
  }, [target]);

  const submit = async () => {
    if (!target) return;
    if (!password) {
      Toast.warning(t("Microsoft account password is required."));
      return;
    }
    if (Boolean(clientId.trim()) !== Boolean(refreshToken.trim())) {
      Toast.warning(t("OAuth client ID and refresh token must be configured together."));
      return;
    }
    setSubmitting(true);
    let nextDetail: AdminMicrosoftResourceDetail;
    try {
      nextDetail = await replaceAdminMicrosoftCredentials(target.id, {
        clientId: clientId.trim() || undefined,
        password,
        refreshToken: refreshToken.trim() || undefined,
        version: target.version,
      });
      Toast.success(t("Credentials replaced and validation queued."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Credential replacement failed."));
      setSubmitting(false);
      return;
    }
    try {
      await onSaved(nextDetail);
    } catch (error) {
      Toast.error(
        getIamErrorMessage(t, error, "Admin Microsoft resources load failed.")
      );
    } finally {
      setSubmitting(false);
    }
    setPassword("");
    setClientId("");
    setRefreshToken("");
    onCancel();
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => void submit()}
      okText={t("Replace credentials")}
      size="small"
      title={t("Replace Microsoft credentials")}
      visible={Boolean(target)}
    >
      {target ? (
        <div className="space-y-4 py-1">
          <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
            {t(
              "All credential fields are write-only. Existing values are never displayed, and submitting replaces the complete credential set."
            )}
          </div>
          <InfoItem
            label={t("Email")}
            value={<span className="font-mono">{target.emailAddress}</span>}
          />
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Password")} *
            </span>
            <Input
              autoComplete="new-password"
              mode="password"
              onChange={(value) => setPassword(String(value))}
              placeholder={t("Enter a replacement password")}
              value={password}
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("OAuth client ID")}
            </span>
            <Input
              autoComplete="off"
              mode="password"
              onChange={(value) => setClientId(String(value))}
              placeholder={t("Optional; must be submitted with a refresh token")}
              value={clientId}
            />
          </label>
          <label className="block">
            <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
              {t("Refresh token")}
            </span>
            <Input
              autoComplete="off"
              mode="password"
              onChange={(value) => setRefreshToken(String(value))}
              placeholder={t("Optional; must be submitted with a client ID")}
              value={refreshToken}
            />
          </label>
        </div>
      ) : null}
    </Modal>
  );
}
