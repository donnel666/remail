import { useMemo, useRef, useState } from "react";
import { Button, Modal, Space, TextArea, Toast, Typography } from "@douyinfe/semi-ui";
import { FileText, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  importMicrosoftResources,
  waitForResourceImport,
} from "@/lib/resources-api";

import { MICROSOFT_EMAIL_FORMAT_HINT } from "./model";

const { Text } = Typography;
const ENTRY_AREA_HEIGHT = 208;

interface ImportMicrosoftEmailsModalProps {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  onSuccess: () => void;
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
  const [text, setText] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const lines = useMemo(
    () =>
      text
        .split("\n")
        .map((line) => line.trim())
        .filter((line) => line.length > 0),
    [text]
  );

  const reset = () => {
    setMode("paste");
    setLifetimeType("long_lived");
    setText("");
    setFile(null);
    setBusy(false);
  };

  const close = () => {
    reset();
    onOpenChange(false);
  };

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
      const uploadFile =
        mode === "paste"
          ? new File([lines.join("\n")], "microsoft-resources.txt", {
              type: "text/plain",
            })
          : file;

      if (!uploadFile) return;

      const result = await importMicrosoftResources(
        uploadFile,
        lifetimeType === "long_lived"
      );
      Toast.success(t("Resource import accepted."));
      const status = await waitForResourceImport(result.importId);
      if (status.status === "failed") {
        throw new Error(t(status.lastSafeError || "Resource import failed."));
      }
      close();
      onSuccess();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Resource import failed."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      footer={
        <Space>
          <Button disabled={busy} onClick={close} theme="outline">
            {t("Cancel")}
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
