import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Modal, Space, TextArea, Toast, Typography } from "@douyinfe/semi-ui";
import { FileText, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import { requireTurnstile } from "@/components/auth/TurnstileGate";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { getICloudImport, importICloudResources, type ICloudImport } from "@/lib/icloud-api";
import { generateIdempotencyKey } from "@/lib/idempotency";

const { Text } = Typography;
const ENTRY_AREA_HEIGHT = 208;

export function ImportICloudEmailsModal({ open, onOpenChange, onSuccess }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSuccess: () => void;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<"paste" | "file">("paste");
  const [text, setText] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [view, setView] = useState<ICloudImport | null>(null);
  const [pollError, setPollError] = useState("");
  const [retry, setRetry] = useState(0);
  const uploadRef = useRef<AbortController | null>(null);
  const attemptRef = useRef<{ content: string; key: string } | null>(null);
  const onSuccessRef = useRef(onSuccess);
  onSuccessRef.current = onSuccess;
  const processing = view?.status === "processing";
  const fileRef = useRef<HTMLInputElement>(null);
  const lines = useMemo(() => text.split(/\r?\n/).filter((line) => line.trim()), [text]);

  const switchButtonClass = (active: boolean) =>
    [
      "flex h-12 w-full items-center justify-center gap-2 rounded-lg border-2 px-4 text-sm font-semibold transition-all",
      active
        ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
        : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] text-[var(--semi-color-text-1)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]",
    ].join(" ");

  useEffect(() => () => uploadRef.current?.abort(), []);
  useEffect(() => {
    if (open) return;
    uploadRef.current?.abort();
    uploadRef.current = null;
    attemptRef.current = null;
    setMode("paste");
    setText("");
    setFile(null);
    setBusy(false);
    setView(null);
    setPollError("");
  }, [open]);
  useEffect(() => {
    if (!open || !view || view.status !== "processing") return;
    const controller = new AbortController();
    const timer = window.setTimeout(async () => {
      try {
        const result = await getICloudImport(view.importId, controller.signal);
        if (controller.signal.aborted) return;
        setPollError("");
        setView(result);
        if (result.status !== "processing") onSuccessRef.current();
      } catch (error) {
        if (!controller.signal.aborted) setPollError(getIamErrorMessage(t, error, "Failed to load import progress."));
      }
    }, 3000);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [open, view, retry, t]);

  const submit = async () => {
    if (uploadRef.current || view) return;
    const controller = new AbortController();
    uploadRef.current = controller;
    setBusy(true);
    try {
      let content = text;
      if (mode === "file") {
        if (!file) return;
        if (file.size > 1_048_576) throw new Error(t("Apple import accepts up to 100 accounts and a 1 MB TXT file."));
        try {
          content = new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer());
        } catch {
          throw new Error(t("Import file must be UTF-8 encoded."));
        }
      }
      const lines = content.replace(/^\uFEFF/, "").split(/\r?\n/).filter((line) => line.trim());
      if (!lines.length || lines.length > 100 || new TextEncoder().encode(content).length > 1_048_576) {
        throw new Error(t("Apple import accepts up to 100 accounts and a 1 MB TXT file."));
      }
      const invalid = lines.findIndex((line) => {
        const parts = line.split("----").map((part) => part.trim());
        if (parts.length !== 3 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(parts[0]) || !parts[1]) return true;
        try {
          const url = new URL(parts[2]);
          return !["http:", "https:"].includes(url.protocol) || Boolean(url.username || url.password || url.hash);
        } catch { return true; }
      });
      if (invalid !== -1) throw new Error(t("Import invalid line", { line: invalid + 1 }));
      if (attemptRef.current?.content !== content) attemptRef.current = { content, key: generateIdempotencyKey() };
      const token = await requireTurnstile("icloud_resource_import", controller.signal);
      if (!token || controller.signal.aborted) return;
      const result = await importICloudResources(new File([content], file?.name ?? "apple-emails.txt", { type: "text/plain" }), attemptRef.current.key, token, controller.signal);
      if (controller.signal.aborted) return;
      setView(result);
      setText("");
      setFile(null);
      attemptRef.current = null;
      Toast.success(t("Resource import accepted."));
      onSuccessRef.current();
    } catch (error) {
      if (!controller.signal.aborted) Toast.error(getIamErrorMessage(t, error, "Resource import failed."));
    } finally {
      if (!controller.signal.aborted) setBusy(false);
      if (uploadRef.current === controller) uploadRef.current = null;
    }
  };

  const close = () => {
    if (busy) return;
    if (processing) Toast.info(t("Resource import continues in background."));
    onOpenChange(false);
  };

  return (
    <Modal
      visible={open}
      title={t("Import Apple Emails")}
      width="min(666px, calc(100vw - 32px))"
      onCancel={close}
      footer={<Space>
        <Button disabled={busy} onClick={close}>{t(processing ? "Continue in background" : view ? "Close" : "Cancel")}</Button>
        {pollError ? <Button onClick={() => { setPollError(""); setRetry((value) => value + 1); }}>{t("Retry")}</Button> : null}
        {!view ? <Button type="primary" loading={busy} disabled={busy || (mode === "paste" ? !lines.length : !file)} onClick={() => void submit()}>{t(busy ? "Importing" : "Import")}</Button> : null}
      </Space>}
    >
      <div className="space-y-4">
        {view ? <div aria-live="polite" role="status" className="space-y-2">
          <p>{t("Apple import progress", { total: view.accepted, completed: view.completed, failed: view.failed, waiting: view.waiting })}</p>
          {processing ? <p>{t("Importing")}</p> : <p>{t(view.failed ? "Apple import has failures" : "Apple import completed")}</p>}
          {pollError ? <p role="alert">{pollError}</p> : null}
          <div className="max-h-64 space-y-2 overflow-y-auto break-words">
            {view.tasks.map((task) => <div key={task.id} className="rounded-lg border border-[var(--semi-color-border)] p-2 text-sm">
              <div>{task.email}</div>
              <div className="text-[var(--semi-color-text-2)]">{t(task.status === "completed" ? "Completed" : task.status === "failed" ? "Failed" : "Importing")}</div>
              {task.lastSafeError ? <div>{task.lastSafeError}</div> : null}
            </div>)}
          </div>
        </div> : <>
          <div className="grid grid-cols-2 gap-2">
            <button
              className={switchButtonClass(mode === "paste")}
              disabled={busy}
              aria-pressed={mode === "paste"}
              onClick={() => { setMode("paste"); setFile(null); }}
              type="button"
            >
              <FileText size={16} />
              {t("Manual input")}
            </button>
            <button
              className={switchButtonClass(mode === "file")}
              disabled={busy}
              aria-pressed={mode === "file"}
              onClick={() => { setMode("file"); setText(""); }}
              type="button"
            >
              <Upload size={16} />
              {t("TXT file")}
            </button>
          </div>
          <div>
            {mode === "paste" ? (
              <TextArea
                aria-label={t("Apple account entries")}
                className="font-mono"
                disabled={busy}
                onChange={setText}
                placeholder="email----password----https://example.com/code"
                rows={8}
                style={{ height: ENTRY_AREA_HEIGHT, resize: "none" }}
                value={text}
              />
            ) : (
              <button
                className="flex w-full flex-col items-center justify-center rounded-xl border border-dashed border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-6 text-center transition-colors hover:bg-[var(--semi-color-fill-1)]"
                disabled={busy}
                onClick={() => fileRef.current?.click()}
                onDragOver={(event) => event.preventDefault()}
                onDrop={(event) => { event.preventDefault(); if (!busy) setFile(event.dataTransfer.files[0] ?? null); }}
                style={{ height: ENTRY_AREA_HEIGHT }}
                type="button"
              >
                <input
                  accept=".txt,text/plain"
                  aria-label={t("TXT file")}
                  className="hidden"
                  disabled={busy}
                  onChange={(event) => setFile(event.target.files?.[0] ?? null)}
                  ref={fileRef}
                  type="file"
                />
                <FileText className="mb-2 size-8 text-[var(--semi-color-text-2)]" />
                <Text strong>{file ? file.name : t("Click to select or drag file here")}</Text>
                <Text size="small" type="tertiary">
                  {file ? `${(file.size / 1024).toFixed(1)} KB` : t("Supports .txt files, one entry per line")}
                </Text>
              </button>
            )}
            <div className="mt-1 min-h-5">
              {mode === "paste" && text.length > 0 ? <Text size="small" type="tertiary">{t("Parsed entries", { count: lines.length })}</Text> : null}
            </div>
          </div>
          <p className="text-xs text-[var(--semi-color-text-2)]">{t("Apple import prerequisites")}</p>
          <p className="text-xs text-[var(--semi-color-text-2)]">{t("Apple import lifetime")}</p>
          <p className="text-xs text-[var(--semi-color-text-2)]">{t("Apple import accepts up to 100 accounts and a 1 MB TXT file.")}</p>
          <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
            <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">{t("Supported format")}</div>
            <pre className="font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">{t("Apple import format")}</pre>
          </div>
        </>}
      </div>
    </Modal>
  );
}
