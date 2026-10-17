import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Modal, Select, TextArea, Toast, Typography } from "@douyinfe/semi-ui";
import { FileText, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import { AdminUserSelect } from "@/components/semi/admin-user-select";
import { importAdminDaumResources, listAdminDaumOwners, waitForAdminDaumResourceImport } from "@/lib/admin-daum-api";
import type { AdminDaumOwner } from "../admin-daum/admin-daum-types";
import { getIamErrorMessage } from "@/lib/iam-errors";
import type { AdminDaumImportResponse, AdminDaumImportErrorStrategy as ImportErrorStrategy } from "../admin-daum/admin-daum-types";
import { DaumImportResultPanel } from "./daum-import-result-panel";

import {
  createMicrosoftImportBatches, runMicrosoftImportBatches,
  type MicrosoftImportBatches, type MicrosoftImportBatchProgress,
} from "./microsoft-import-batches";

const { Text } = Typography;
const ENTRY_AREA_HEIGHT = 208;
// shortcut: lower configured import limits are enforced by the server.
const MAX_IMPORT_FILE_BYTES = 512 * 1024 * 1024;

interface ImportDaumEmailsModalProps {
  open: boolean;
  owners?: AdminDaumOwner[];
  onOpenChange: (value: boolean) => void;
  onSuccess: () => void | Promise<void>;
}

export function ImportDaumEmailsModal({
  open,
  owners = [],
  onOpenChange,
  onSuccess,
}: ImportDaumEmailsModalProps) {
  const { t } = useTranslation();
  const [protocol, setProtocol] = useState<"imap" | "pop3">("imap");
  const [results, setResults] = useState<AdminDaumImportResponse[]>([]);
  const [selectedImportId, setSelectedImportId] = useState<number>();
  const result = results.find((item) => item.importId === selectedImportId) ?? results[0];
  const updateResult = useCallback((next: AdminDaumImportResponse) => {
    setResults((current) => current.some((item) => item.importId === next.importId)
      ? current.map((item) => item.importId === next.importId ? next : item)
      : [...current, next]);
  }, []);
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [mode, setMode] = useState<"paste" | "file">("paste");
  const [lifetimeType, setLifetimeType] = useState<"long_lived" | "short_lived">(
    "long_lived"
  );
  const [errorStrategy, setErrorStrategy] =
    useState<ImportErrorStrategy>("skip");
  const [text, setText] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<MicrosoftImportBatchProgress | null>(null);
  const batchesRef = useRef<MicrosoftImportBatches | null>(null);
  const polling = Boolean(busy && progress?.polling && progress.current === progress.total);
  const fileRef = useRef<HTMLInputElement>(null);
  const importPollAbortRef = useRef<AbortController | null>(null);
  const previousOpen = useRef(false);
  useEffect(() => { batchesRef.current = null; setProgress(null); }, [text, file, ownerId, lifetimeType, errorStrategy, protocol]);

  useEffect(() => () => importPollAbortRef.current?.abort(), []);
  useEffect(() => {
    const opened = open && !previousOpen.current;
    previousOpen.current = open;
    if (!opened) return;
    setOwnerId(undefined);
    setMode("paste");
    setLifetimeType("long_lived");
    setErrorStrategy("skip");
    setProtocol("imap");
    setText("");
    setFile(null);
  }, [open]);
  useEffect(() => { if (open && ownerId === undefined) setOwnerId(owners.find((owner) => owner.enabled)?.id); }, [open, ownerId, owners]);

  const lines = useMemo(
    () =>
      text
        .split("\n")
        .map((line) => line.trim())
        .filter((line) => line.length > 0),
    [text]
  );

  const reset = () => {
    importPollAbortRef.current?.abort();
    importPollAbortRef.current = null;
    setMode("paste");
    setLifetimeType("long_lived");
    setErrorStrategy("skip");
    setProtocol("imap");
    setText("");
    setFile(null);
    setBusy(false);
    setProgress(null);
    batchesRef.current = null;
  };

  const close = () => {
    if (busy && !polling) return;
    if (polling) {
      Toast.info(t("Resource import continues in background."));
    }
    reset();
    onOpenChange(false);
  };

  useEffect(() => {
    if (open) return;
    importPollAbortRef.current?.abort();
    importPollAbortRef.current = null;
    setProgress(null);
    setBusy(false);
  }, [open]);

  const switchButtonClass = (active: boolean) =>
    [
      "flex h-12 w-full items-center justify-center gap-2 rounded-lg border-2 px-4 text-sm font-semibold transition-all",
      active
        ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
        : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] text-[var(--semi-color-text-1)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]",
    ].join(" ");

  const selectFile = (selected: File | null) => {
    // Keep the File in state, but allow selecting the same native path again.
    if (fileRef.current) fileRef.current.value = "";
    if (busy || importPollAbortRef.current) return;
    if (selected && !selected.name.toLowerCase().endsWith(".txt")) {
      setFile(null);
      Toast.warning(t("Please select a TXT file."));
      return;
    }
    if (selected && selected.size > MAX_IMPORT_FILE_BYTES) {
      setFile(null);
      Toast.warning(t("Import file must not exceed {{max}} MiB.", { max: MAX_IMPORT_FILE_BYTES / 1024 / 1024 }));
      return;
    }
    setFile(selected);
  };

  const handleImport = async () => {
    if (busy || importPollAbortRef.current || batchesRef.current?.failed) return;
    if (!ownerId) { Toast.warning(t("Please select an owner.")); return; }
    if (mode === "paste" ? lines.length === 0 : !file) {
      Toast.warning(t(mode === "file" ? "Please select a TXT file." : "Please enter Daum resources."));
      return;
    }
    const controller = new AbortController();
    importPollAbortRef.current = controller;
    const isCurrentImport = () => importPollAbortRef.current === controller && !controller.signal.aborted;
    setBusy(true);
    try {
      if (!batchesRef.current) {
        let sourceText = text;
        if (mode === "file" && file) {
          const bytes = await file.arrayBuffer();
          if (!isCurrentImport()) return;
          try { sourceText = new TextDecoder("utf-8", { fatal: true }).decode(bytes); }
          catch { Toast.error(t("Import file must be UTF-8 encoded.")); return; }
        }
        if (!sourceText.trim()) { Toast.warning(t("Please enter Daum resources.")); return; }
        batchesRef.current = createMicrosoftImportBatches(sourceText);
        setResults([]);
        setSelectedImportId(undefined);
      }
      const batches = batchesRef.current;
      await runMicrosoftImportBatches(batches, {
        upload: async (content, _index, idempotencyKey) => {
          const accepted = await importAdminDaumResources({ content, ownerId, longLived: lifetimeType === "long_lived", errorStrategy, protocol }, controller.signal, idempotencyKey);
          if (isCurrentImport()) {
            updateResult(accepted);
            setSelectedImportId((current) => current ?? accepted.importId);
          }
          return accepted;
        },
        idempotentUploads: true,
        poll: (id) => waitForAdminDaumResourceImport(id, { signal: controller.signal, onProgress: (next) => { if (isCurrentImport()) updateResult(next); } }),
        onProgress: (next) => { if (isCurrentImport()) setProgress(next); },
        onResult: (status) => {
          if (isCurrentImport() && status.skipped) Toast.warning(t("Import skipped errors", { count: status.skipped }));
        },
        signal: controller.signal,
      });
      if (!isCurrentImport()) return;
      Toast.success(t("Daum resources imported.", { count: batches.imported }));
      setText("");
      setFile(null);
      await onSuccess();
    } catch (error) {
      if (!isCurrentImport() || isAbortError(error)) return;
      Toast.error(getIamErrorMessage(t, error, "Resource import failed."));
      if (batchesRef.current?.imported) {
        Toast.warning(t("Completed import batches are kept. Remaining batches have not been uploaded."));
        try { await onSuccess(); }
        catch (refreshError) { if (isCurrentImport()) Toast.error(getIamErrorMessage(t, refreshError, "Admin Daum resources load failed.")); }
      }
    } finally {
      if (isCurrentImport()) {
        importPollAbortRef.current = null;
        setBusy(false);
      }
    }
  };

  return (
    <Modal
      bodyStyle={{ maxHeight: "calc(100dvh - 180px)", overflowY: "auto" }}
      centered
      cancelText={t(polling ? "Continue in background" : "Cancel")}
      cancelButtonProps={{ disabled: busy && !polling }}
      confirmLoading={busy}
      onOk={() => void handleImport()}
      okButtonProps={{ disabled: batchesRef.current?.failed || (mode === "paste" ? lines.length === 0 : !file) }}
      okText={t("Import")}
      onCancel={close}
      title={t("Import Daum Emails")}
      visible={open}
      width="min(666px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        {progress ? <div role="status" aria-live="polite" className="text-xs text-[var(--semi-color-text-2)]">
          {t("Import batch progress", { current: progress.current, total: progress.total, completed: batchesRef.current?.completed ?? 0 })}
          {batchesRef.current?.failed ? <p>{t("This batch failed. Correct the remaining data before importing again.")}</p> : null}
        </div> : null}
        <p className="text-xs text-[var(--semi-color-text-2)]">{t("Large files are split into batches of up to 99 MB. Keep this dialog open until all batches are uploaded.")}</p>
        {results.length > 1 ? <Select aria-label={t("Import result")} style={{ width: "100%" }} value={result?.importId} onChange={(value) => setSelectedImportId(Number(value))}>
          {results.map((item) => <Select.Option key={item.importId} value={item.importId}>{t("Import result")} #{item.importId}</Select.Option>)}
        </Select> : null}
        {result ? <DaumImportResultPanel importId={result.importId} result={result} onProgress={updateResult} poll={open && !busy && result.status === "processing"} /> : null}
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Owner")} *</span>
          <AdminUserSelect
            value={ownerId}
            onChange={(value) => { if (!busy) setOwnerId(value); }}
            options={owners.map((owner) => ({ value: owner.id, label: owner.email + " · " + owner.nickname, disabled: busy || !owner.enabled, data: owner }))}
            loadOptions={async (search) => (await listAdminDaumOwners(search)).map((owner) => ({ value: owner.id, label: owner.email + " · " + owner.nickname, disabled: busy || !owner.enabled, data: owner }))}
            placeholder={t("Search user by email, nickname or ID")}
            emptyContent={t("No users found")}
            style={{ width: "100%" }}
          />
        </label>
        <div className="grid grid-cols-2 gap-2">
          <button
            aria-pressed={mode === "paste"}
            className={switchButtonClass(mode === "paste")}
            disabled={busy}
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
            disabled={busy}
            onClick={() => {
              setMode("file");
              setText("");
            }}
            type="button"
          >
            <Upload size={16} />
            {t("TXT file")}
          </button>
        </div>

        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">{t("Mail protocol")}</span>
          <Select aria-label={t("Mail protocol")} value={protocol} disabled={busy} style={{ width: "100%" }} onChange={(value) => setProtocol(value === "pop3" ? "pop3" : "imap")}>
            <Select.Option value="imap">IMAP · imap.daum.net:993 (TLS)</Select.Option>
            <Select.Option value="pop3">POP3 · pop.daum.net:995 (TLS)</Select.Option>
          </Select>
          <div className="mt-1 text-xs text-[var(--semi-color-text-2)]">{t("Daum APP passwords are used for mail access. IMAP is recommended for complete history scanning.")}</div>
        </label>
        <div className="grid grid-cols-2 gap-2">
          <button
            disabled={busy}
            className={switchButtonClass(lifetimeType === "long_lived")}
            onClick={() => setLifetimeType("long_lived")}
            type="button"
          >
            {t("Long-lived")}
          </button>
          <button
            disabled={busy}
            className={switchButtonClass(lifetimeType === "short_lived")}
            onClick={() => setLifetimeType("short_lived")}
            type="button"
          >
            {t("Short-lived")}
          </button>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <button
            disabled={busy}
            className={switchButtonClass(errorStrategy === "skip")}
            onClick={() => setErrorStrategy("skip")}
            type="button"
          >
            {t("Skip errors")}
          </button>
          <button
            disabled={busy}
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
              <span>{t("Daum resource entries")} *</span>
              <Text size="small" type="tertiary">
                {t("Parsed entries", { count: lines.length })}
              </Text>
            </span>
            <TextArea
              disabled={busy}
              className="font-mono"
              onChange={(value) => setText(value)}
              placeholder={t("Daum import formats")}
              rows={8}
              style={{ height: ENTRY_AREA_HEIGHT, resize: "none" }}
              value={text}
            />
          </label>
        ) : (
            <button
              className="flex w-full flex-col items-center justify-center rounded-xl border border-dashed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-6 text-center transition-colors hover:bg-[var(--semi-color-fill-1)]"
              disabled={busy}
              onClick={() => fileRef.current?.click()}
              onDragOver={(event) => event.preventDefault()}
              onDrop={(event) => {
                event.preventDefault();
                const dropped = event.dataTransfer.files[0];
                if (dropped) selectFile(dropped);
              }}
              style={{ height: ENTRY_AREA_HEIGHT }}
              type="button"
            >
              <input
                accept=".txt"
                aria-label={t("Select TXT file")}
                className="hidden"
                disabled={busy}
                onChange={(event) => selectFile(event.target.files?.[0] ?? null)}
                onClick={(event) => event.stopPropagation()}
                ref={fileRef}
                type="file"
              />
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
        )}

        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
            {t("Supported format")}
          </div>
          <pre className="overflow-x-auto whitespace-pre-wrap break-all font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            {t("Daum import formats")}
          </pre>
        </div>
        <div className="text-xs text-[var(--semi-color-text-2)]">{t("Daum five-field imports use the fourth field URL for auxiliary pickup; the fifth field is the independent Kakao account email.")}</div>
        <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
          {t("Credentials are accepted as write-only input. Passwords and tokens are never returned by this page.")}
        </div>
      </div>
    </Modal>
  );
}

function isAbortError(error: unknown) {
  return error instanceof DOMException && error.name === "AbortError";
}
