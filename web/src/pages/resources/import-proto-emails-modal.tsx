import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Modal, Space, TextArea, Toast, Typography } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";
import { FileText, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useAuth } from "@/context/auth-provider";
import { AdminUserSelect } from "@/components/semi/admin-user-select";
import { getAdminProtoResourceImport, importAdminProtoResources, listAdminProtoOwners, waitForAdminProtoResourceImport } from "@/lib/admin-proto-api";
import type { AdminProtoOwner } from "../admin-proto/admin-proto-types";
import { ProtoImportResultPanel } from "./proto-import-result-panel";
import { requireTurnstile } from "@/components/auth/TurnstileGate";
import { getApiErrorBodyMessage, getIamErrorMessage } from "@/lib/iam-errors";
import {
  importProtoResources,
  getProtoResourceImport,
  type ProtoImportResponse,
  type ImportErrorStrategy,
  waitForResourceImport,
} from "@/lib/proto-api";

import { PROTO_EMAIL_FORMAT_HINT } from "./proto-model";
import {
  preprocessProtoImportContent,
  type ProtoImportPreprocessFailure,
} from "./proto-import-preprocess";

const { Text } = Typography;
const ENTRY_AREA_HEIGHT = 208;
const SKIPPED_IMPORT_ENTRIES_PATTERN = /^Skipped (\d+) import entr(?:y|ies)\.$/;

interface ImportProtoEmailsModalProps {
  open: boolean;
  admin?: boolean;
  owners?: AdminProtoOwner[];
  onOpenChange: (value: boolean) => void;
  onSuccess: () => void | Promise<void>;
}

export function ImportProtoEmailsModal({
  open,
  admin = false,
  owners = [],
  onOpenChange,
  onSuccess,
}: ImportProtoEmailsModalProps) {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const storageKey = `remail.proto.import.${admin ? "admin" : "owned"}.${currentUser?.id ?? 0}`;
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [result, setResult] = useState<ProtoImportResponse | null>(null);
  const [importId, setImportId] = useState<number | null>(null);
  useEffect(() => { if (admin && open && ownerId === undefined) setOwnerId(owners.find((owner) => owner.enabled)?.id); }, [admin, open, ownerId, owners]);
  useEffect(() => {
    if (!open) return;
    const saved = Number(sessionStorage.getItem(storageKey));
    if (!Number.isInteger(saved) || saved <= 0) return;
    const controller = new AbortController();
    setImportId(saved);
    void (admin ? getAdminProtoResourceImport(saved, controller.signal) : getProtoResourceImport(saved, controller.signal)).then((value) => { if (!controller.signal.aborted) setResult(value); }).catch(() => undefined);
    return () => controller.abort();
  }, [admin, open, storageKey]);
  const [mode, setMode] = useState<"paste" | "file">("paste");
  const [lifetimeType, setLifetimeType] = useState<"long_lived" | "short_lived">(
    "long_lived"
  );
  const [errorStrategy, setErrorStrategy] =
    useState<ImportErrorStrategy>("skip");
  const [text, setText] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [polling, setPolling] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const importPollAbortRef = useRef<AbortController | null>(null);

  useEffect(() => () => importPollAbortRef.current?.abort(), []);

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
    setText("");
    setFile(null);
    setBusy(false);
    setPolling(false);
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
    setPolling(false);
    setBusy(false);
  }, [open]);

  const switchButtonClass = (active: boolean) =>
    [
      "flex h-12 w-full items-center justify-center gap-2 rounded-lg border-2 px-4 text-sm font-semibold transition-all",
      active
        ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
        : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] text-[var(--semi-color-text-1)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]",
    ].join(" ");

  const handleImport = async () => {
    if (busy || (lines.length === 0 && !file)) return;
    if (admin && !ownerId) { Toast.warning(t("Please select an owner.")); return; }
    setBusy(true);
    try {
      const sourceText = mode === "paste" ? text : file ? new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer()) : "";
      const sourceName =
        mode === "paste" ? "proto-resources.txt" : file?.name;
      if (!sourceName) return;

      const prepared = preprocessProtoImportContent(
        sourceText,
        errorStrategy
      );
      if (prepared.firstFailure) {
        Toast.error(
          getImportPreprocessFailureMessage(t, prepared.firstFailure)
        );
        return;
      }
      if (prepared.validCount === 0) {
        throw new Error("No valid import entries.");
      }
      if (prepared.skippedCount > 0) {
        Toast.warning(
          t("Import skipped errors", { count: prepared.skippedCount })
        );
      }

      const uploadFile = new File([sourceText], sourceName, {
        type: "text/plain",
      });

      // Challenged only after preprocessing passes, so a file that fails
      // validation never costs the user a verification.
      const turnstileToken = admin ? "" : await requireTurnstile("resource_import");
      if (!admin && !turnstileToken) return;
      const controller = new AbortController();
      importPollAbortRef.current = controller;
      const accepted = admin
        ? await importAdminProtoResources({ content: sourceText, ownerId: ownerId!, longLived: lifetimeType === "long_lived", errorStrategy }, controller.signal)
        : await importProtoResources(uploadFile, lifetimeType === "long_lived", turnstileToken || "", errorStrategy, controller.signal);
      setImportId(accepted.importId);
      setResult(accepted);
      sessionStorage.setItem(storageKey, String(accepted.importId));
      Toast.success(t("Resource import accepted."));
      setPolling(true);
      const status = accepted.status === "processing"
        ? await (admin ? waitForAdminProtoResourceImport : waitForResourceImport)(accepted.importId, { signal: controller.signal, onProgress: setResult })
        : accepted;
      setResult(status);
      if (status.status === "failed") {
        if (status.imported > 0) await onSuccess();
        throw new Error(status.lastSafeError || "Resource import failed.");
      }
      if (status.lastSafeError) {
        Toast.warning(getImportWarningMessage(t, status.lastSafeError));
      }
      setText("");
      setFile(null);
      await onSuccess();
    } catch (error) {
      if (isAbortError(error)) return;
      Toast.error(getIamErrorMessage(t, error, "Resource import failed."));
    } finally {
      importPollAbortRef.current = null;
      setPolling(false);
      setBusy(false);
    }
  };

  return (
    <Modal
      footer={
        <Space>
          <Button disabled={busy && !polling} onClick={close} theme="outline">
            {polling ? t("Continue in background") : t("Cancel")}
          </Button>
          <Button
            disabled={mode === "paste" ? lines.length === 0 : !file}
            loading={busy}
            onClick={handleImport}
            type="primary"
          >
            {busy ? t("Importing") : t("Import")}
          </Button>
        </Space>
      }
      onCancel={close}
      title={t("Import Proto Emails")}
      visible={open}
      width="min(666px, calc(100vw - 32px))"
    >
      <div className="space-y-4">
        {admin ? <label className="block">
          <span className="mb-1.5 block text-sm font-medium">{t("Owner")} *</span>
          <AdminUserSelect
            value={ownerId}
            onChange={(value) => setOwnerId(value)}
            options={owners.map((owner) => ({ value: owner.id, label: owner.email + " · " + owner.nickname, disabled: !owner.enabled, data: owner }))}
            loadOptions={async (search) => (await listAdminProtoOwners(search)).map((owner) => ({ value: owner.id, label: owner.email + " · " + owner.nickname, disabled: !owner.enabled, data: owner }))}
            placeholder={t("Search user by email, nickname or ID")}
            emptyContent={t("No users found")}
            style={{ width: "100%" }}
          />
        </label> : null}
        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(mode === "paste")}
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
            className={switchButtonClass(mode === "file")}
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

        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(lifetimeType === "long_lived")}
            onClick={() => setLifetimeType("long_lived")}
            type="button"
          >
            {t("Long-lived")}
          </button>
          <button
            className={switchButtonClass(lifetimeType === "short_lived")}
            onClick={() => setLifetimeType("short_lived")}
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

        <div>
          {mode === "paste" ? (
            <TextArea
              className="font-mono"
              onChange={(value) => setText(value)}
              placeholder="email----password"
              rows={8}
              style={{ height: ENTRY_AREA_HEIGHT, resize: "none" }}
              value={text}
            />
          ) : (
            <button
              className="flex w-full flex-col items-center justify-center rounded-xl border border-dashed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-6 text-center transition-colors hover:bg-[var(--semi-color-fill-1)]"
              onClick={() => fileRef.current?.click()}
              style={{ height: ENTRY_AREA_HEIGHT }}
              type="button"
            >
              <input
                accept=".txt"
                className="hidden"
                onChange={(event) => setFile(event.target.files?.[0] ?? null)}
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
          <div className="mt-1 min-h-5">
            {mode === "paste" && text.length > 0 ? (
              <Text size="small" type="tertiary">
                {t("Parsed entries", { count: lines.length })}
              </Text>
            ) : null}
          </div>
        </div>

        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
            {t("Supported format")}
          </div>
          <pre className="font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            {PROTO_EMAIL_FORMAT_HINT}
          </pre>
          <div className="mt-2 text-xs text-[var(--semi-color-text-2)]">
            {t("Passwords are write-only. Lifetime is a resource classification.")}
          </div>
        </div>
        {open && importId ? <ProtoImportResultPanel admin={admin} importId={importId} result={result} onProgress={setResult} poll={!polling} /> : null}
      </div>
    </Modal>
  );
}

export function getImportWarningMessage(t: TFunction, safeMessage: string) {
  const match = SKIPPED_IMPORT_ENTRIES_PATTERN.exec(safeMessage);
  if (match) {
    return t("Import skipped errors", { count: Number(match[1]) });
  }
  return getApiErrorBodyMessage(
    t,
    { message: safeMessage },
    "Resource import completed with warnings."
  );
}

function isAbortError(error: unknown) {
  return error instanceof DOMException && error.name === "AbortError";
}

function getImportPreprocessFailureMessage(
  t: TFunction,
  failure: ProtoImportPreprocessFailure
) {
  if (failure.category === "duplicate_email") {
    return t("Import duplicate line", {
      line: failure.line,
      firstLine: failure.firstLine,
    });
  }
  if (failure.line === 0) {
    return t("No valid import entries.");
  }
  return t("Import invalid line", { line: failure.line });
}
