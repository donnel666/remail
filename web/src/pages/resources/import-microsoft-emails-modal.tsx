import { useMemo, useRef, useState } from "react";
import { Button, Modal, Space, TextArea, Toast, Typography } from "@douyinfe/semi-ui";
import { FileText, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import { MICROSOFT_EMAIL_FORMAT_HINT } from "./model";

const { Text } = Typography;

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
  const [text, setText] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const lines = useMemo(
    () =>
      text
        .split("\n")
        .map((line) => line.trim())
        .filter((line) => line.length > 0 && !line.startsWith("#")),
    [text]
  );

  const reset = () => {
    setMode("paste");
    setText("");
    setFile(null);
    setBusy(false);
  };

  const close = () => {
    reset();
    onOpenChange(false);
  };

  const handleImport = async () => {
    if (lines.length === 0 && !file) return;
    setBusy(true);
    await new Promise((resolve) => setTimeout(resolve, 800));
    Toast.success(t("Resources imported successfully"));
    setBusy(false);
    close();
    onSuccess();
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
        <Space>
          <Button
            icon={<FileText size={14} />}
            onClick={() => {
              setMode("paste");
              setFile(null);
            }}
            theme={mode === "paste" ? "solid" : "light"}
            type={mode === "paste" ? "primary" : "tertiary"}
          >
            {t("Manual input")}
          </Button>
          <Button
            icon={<Upload size={14} />}
            onClick={() => {
              setMode("file");
              setText("");
            }}
            theme={mode === "file" ? "solid" : "light"}
            type={mode === "file" ? "primary" : "tertiary"}
          >
            {t("TXT file")}
          </Button>
        </Space>

        {mode === "paste" ? (
          <TextArea
            autosize={{ minRows: 7, maxRows: 10 }}
            className="font-mono"
            onChange={(value) => setText(value)}
            placeholder="email----password"
            value={text}
          />
        ) : (
          <button
            className="flex min-h-[160px] w-full flex-col items-center justify-center rounded-xl border border-dashed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-6 text-center transition-colors hover:bg-[var(--semi-color-fill-1)]"
            onClick={() => fileRef.current?.click()}
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

        {mode === "paste" && text.length > 0 ? (
          <Text size="small" type="tertiary">
            {t("Parsed entries", { count: lines.length })}
          </Text>
        ) : null}

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
