import { useEffect, useMemo, useRef, useState } from "react";
import { Modal, TextArea, Toast, Typography } from "@douyinfe/semi-ui";
import { FileText, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  AdminUserSelect,
  type AdminUserSelectOption,
} from "@/components/semi/admin-user-select";
import {
  importAdminDaumResources,
  listAdminDaumOwners,
  waitForAdminDaumResourceImport,
} from "@/lib/admin-daum-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { ownerRoleLabel } from "../admin-daum/daum-meta";
import type { AdminDaumImportErrorStrategy, AdminDaumOwner } from "../admin-daum/admin-daum-types";
import {
  createMicrosoftImportBatches,
  runMicrosoftImportBatches,
  type MicrosoftImportBatches,
  type MicrosoftImportBatchProgress,
} from "./microsoft-import-batches";

const { Text } = Typography;
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

export function ImportDaumEmailsModal({
  onOpenChange,
  onSuccess: onImported,
  owners = [],
  open: visible,
}: {
  onOpenChange: (open: boolean) => void;
  onSuccess: () => void | Promise<void>;
  owners?: AdminDaumOwner[];
  open: boolean;
}) {
  const onCancel = () => onOpenChange(false);
  const { t } = useTranslation();
  const [mode, setMode] = useState<"paste" | "file">("paste");
  const [content, setContent] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [longLived, setLongLived] = useState(true);
  const [errorStrategy, setErrorStrategy] =
    useState<AdminDaumImportErrorStrategy>("skip");
  const [submitting, setSubmitting] = useState(false);
  const [progress, setProgress] = useState<MicrosoftImportBatchProgress | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const importAbortRef = useRef<AbortController | null>(null);
  const batchesRef = useRef<MicrosoftImportBatches | null>(null);
  const polling = submitting && progress?.polling && progress.current === progress.total;

  useEffect(() => {
    if (!visible) return;
    setMode("paste");
    setContent("");
    setFile(null);
    setOwnerId(undefined);
    setLongLived(true);
    setErrorStrategy("skip");
    setSubmitting(false);
    setProgress(null);
    batchesRef.current = null;
    return () => {
      importAbortRef.current?.abort();
      importAbortRef.current = null;
    };
  }, [visible]);

  useEffect(() => {
    batchesRef.current = null;
    setProgress(null);
  }, [content, file, ownerId, longLived, errorStrategy]);

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
    setProgress(null);
    batchesRef.current = null;
    onCancel();
  };

  const submit = async () => {
    if (submitting || importAbortRef.current || batchesRef.current?.failed) return;
    if (!ownerId) {
      Toast.warning(t("Please select an owner."));
      return;
    }
    if (mode === "paste" ? lines.length === 0 : !file) {
      Toast.warning(
        t(mode === "file" ? "Please select a TXT file." : "Please enter Daum resources.")
      );
      return;
    }
    const controller = new AbortController();
    importAbortRef.current = controller;
    const isCurrentImport = () =>
      importAbortRef.current === controller && !controller.signal.aborted;
    setSubmitting(true);
    try {
      if (!batchesRef.current) {
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
          Toast.warning(t("Please enter Daum resources."));
          return;
        }
        batchesRef.current = createMicrosoftImportBatches(sourceContent);
      }
      const batches = batchesRef.current;
      await runMicrosoftImportBatches(batches, {
        upload: (chunk, _index, idempotencyKey) => importAdminDaumResources({
          content: chunk, errorStrategy, longLived, ownerId, protocol: "imap",
        }, controller.signal, idempotencyKey),
        idempotentUploads: true,
        poll: (id) => waitForAdminDaumResourceImport(id, { signal: controller.signal }),
        onProgress: setProgress,
        onResult: (status) => {
          if (status.skipped) Toast.warning(t("Import skipped errors", { count: status.skipped }));
        },
        signal: controller.signal,
      });
      if (!isCurrentImport()) return;
      Toast.success(
        t("Daum resources imported.", {
          count: batches.imported,
        })
      );
      try {
        await onImported();
      } catch (error) {
        if (isCurrentImport()) {
          Toast.error(getIamErrorMessage(t, error, "Admin Daum resources load failed."));
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
        if (batchesRef.current?.imported) {
          Toast.warning(t("Completed import batches are kept. Remaining batches have not been uploaded."));
          try {
            await onImported();
          } catch (refreshError) {
            if (isCurrentImport()) Toast.error(getIamErrorMessage(t, refreshError, "Admin Daum resources load failed."));
          }
        }
      }
    } finally {
      if (isCurrentImport()) {
        importAbortRef.current = null;
        setSubmitting(false);
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
      okButtonProps={{ disabled: batchesRef.current?.failed || (mode === "paste" ? lines.length === 0 : !file) }}
      okText={t("Import")}
      title={t("Import Daum Emails")}
      visible={visible}
      width="min(666px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        <label className="block" inert={submitting}>
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

        {progress ? (
          <div aria-live="polite" className="text-xs text-[var(--semi-color-text-2)]" role="status">
            {t("Import batch progress", { current: progress.current, total: progress.total, completed: batchesRef.current?.completed ?? 0 })}
            {batchesRef.current?.failed ? <p>{t("This batch failed. Correct the remaining data before importing again.")}</p> : null}
          </div>
        ) : null}
        <p className="text-xs text-[var(--semi-color-text-2)]">
          {t("Large files are split into batches of up to 99 MB. Keep this dialog open until all batches are uploaded.")}
        </p>

        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(longLived)}
            disabled={submitting}
            onClick={() => setLongLived(true)}
            type="button"
          >
            {t("Long-lived")}
          </button>
          <button
            className={switchButtonClass(!longLived)}
            disabled={submitting}
            onClick={() => setLongLived(false)}
            type="button"
          >
            {t("Short-lived")}
          </button>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(errorStrategy === "skip")}
            disabled={submitting}
            onClick={() => setErrorStrategy("skip")}
            type="button"
          >
            {t("Skip errors")}
          </button>
          <button
            className={switchButtonClass(errorStrategy === "abort")}
            disabled={submitting}
            onClick={() => setErrorStrategy("abort")}
            type="button"
          >
            {t("Abort on error")}
          </button>
        </div>

        {mode === "paste" ? (
          <label className="block">
            <span className="mb-1.5 flex items-center justify-between text-sm font-medium text-[var(--semi-color-text-0)]">
              <span>{t("Daum resource entries")} *</span>
              <Text size="small" type="tertiary">
                {t("Parsed entries", { count: lines.length })}
              </Text>
            </span>
            <TextArea
              className="font-mono"
              disabled={submitting}
              onChange={(value) => setContent(value)}
              placeholder="email----APP password"
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
            {t("Daum import formats")}
          </pre>
        </div>

        <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
          {t(
            "Credentials are accepted as write-only input. Passwords and tokens are never returned by this page."
          )}
        </div>
      </div>
    </Modal>
  );
}
