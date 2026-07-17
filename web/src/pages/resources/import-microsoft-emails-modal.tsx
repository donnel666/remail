import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Modal, Space, TextArea, Toast, Typography } from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";
import { FileText, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  importMicrosoftResources,
  type ImportErrorStrategy,
  waitForResourceImport,
} from "@/lib/resources-api";

import { MICROSOFT_EMAIL_FORMAT_HINT } from "./model";
import {
  preprocessMicrosoftImportContent,
  type MicrosoftImportPreprocessFailure,
} from "./microsoft-import-preprocess";

const { Text } = Typography;
const ENTRY_AREA_HEIGHT = 208;
const SKIPPED_IMPORT_ENTRIES_PATTERN = /^Skipped (\d+) import entries?\.$/;

interface ImportMicrosoftEmailsModalProps {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  onSuccess: () => void | Promise<void>;
}

export function ImportMicrosoftEmailsModal({
  open,
  onOpenChange,
  onSuccess,
}: ImportMicrosoftEmailsModalProps) {
  const { t } = useTranslation();
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
    if (lines.length === 0 && !file) return;
    setBusy(true);
    try {
      const sourceText = mode === "paste" ? text : (await file?.text()) ?? "";
      const sourceName =
        mode === "paste" ? "microsoft-resources.txt" : file?.name;
      if (!sourceName) return;

      const prepared = preprocessMicrosoftImportContent(
        sourceText,
        errorStrategy
      );
      if (prepared.firstFailure) {
        throw new Error(
          getImportPreprocessFailureMessage(t, prepared.firstFailure)
        );
      }
      if (prepared.validCount === 0) {
        throw new Error(t("No valid import entries."));
      }
      if (prepared.skippedCount > 0) {
        Toast.warning(
          t("Import skipped errors", { count: prepared.skippedCount })
        );
      }

      const uploadFile = new File([prepared.content], sourceName, {
        type: "text/plain",
      });

      const result = await importMicrosoftResources(
        uploadFile,
        lifetimeType === "long_lived",
        errorStrategy
      );
      Toast.success(t("Resource import accepted."));
      const controller = new AbortController();
      importPollAbortRef.current = controller;
      setPolling(true);
      const status = await waitForResourceImport(result.importId, {
        signal: controller.signal,
      });
      if (status.status === "failed") {
        throw new Error(t(status.lastSafeError || "Resource import failed."));
      }
      if (status.lastSafeError) {
        Toast.warning(getImportWarningMessage(t, status.lastSafeError));
      }
      close();
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
      title={t("Import Microsoft Emails")}
      visible={open}
    >
      <div className="space-y-4">
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
            {MICROSOFT_EMAIL_FORMAT_HINT}
          </pre>
        </div>
      </div>
    </Modal>
  );
}

function getImportWarningMessage(t: TFunction, safeMessage: string) {
  const match = SKIPPED_IMPORT_ENTRIES_PATTERN.exec(safeMessage);
  if (match) {
    return t("Import skipped errors", { count: Number(match[1]) });
  }
  return t(safeMessage);
}

function isAbortError(error: unknown) {
  return error instanceof DOMException && error.name === "AbortError";
}

function getImportPreprocessFailureMessage(
  t: TFunction,
  failure: MicrosoftImportPreprocessFailure
) {
  if (failure.category === "duplicate_email") {
    return t("Import duplicate line", {
      line: failure.line,
      firstLine: failure.firstLine,
    });
  }
  if (failure.category === "non_microsoft_domain") {
    return t("Import non-microsoft line", { line: failure.line });
  }
  if (failure.line === 0) {
    return t("No valid import entries.");
  }
  return t("Import invalid line", { line: failure.line });
}
