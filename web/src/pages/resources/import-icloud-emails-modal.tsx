import { useEffect, useRef, useState } from "react";
import { Button, Modal, Space, TextArea, Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { requireTurnstile } from "@/components/auth/TurnstileGate";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { getICloudImport, importICloudResources, type ICloudImport } from "@/lib/icloud-api";
import { generateIdempotencyKey } from "@/lib/idempotency";

export function ImportICloudEmailsModal({ onClose, onAccepted }: {
  onClose: () => void;
  onAccepted: () => void;
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
  const onAcceptedRef = useRef(onAccepted);
  onAcceptedRef.current = onAccepted;
  const processing = view?.status === "processing";

  useEffect(() => () => uploadRef.current?.abort(), []);
  useEffect(() => {
    if (!view || view.status !== "processing") return;
    const controller = new AbortController();
    const timer = window.setTimeout(async () => {
      try {
        const result = await getICloudImport(view.importId, controller.signal);
        if (controller.signal.aborted) return;
        setPollError("");
        setView(result);
        if (result.status !== "processing") onAcceptedRef.current();
      } catch (error) {
        if (!controller.signal.aborted) setPollError(getIamErrorMessage(t, error, "Failed to load import progress."));
      }
    }, 3000);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [view, retry, t]);

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
      onAcceptedRef.current();
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
    onClose();
  };

  return (
    <Modal visible title={t("Import Apple Emails")} width="min(666px, calc(100vw - 32px))" onCancel={close}
      footer={<Space>
        <Button disabled={busy} onClick={close}>{t(processing ? "Continue in background" : view ? "Close" : "Cancel")}</Button>
        {pollError ? <Button onClick={() => { setPollError(""); setRetry((value) => value + 1); }}>{t("Retry")}</Button> : null}
        {!view ? <Button type="primary" loading={busy} disabled={busy || (mode === "paste" ? !text.trim() : !file)} onClick={() => void submit()}>{t("Import")}</Button> : null}
      </Space>}>
      <div className="space-y-4">
        <p className="text-sm text-[var(--semi-color-text-2)]">{t("Apple import prerequisites")}</p>
        <p className="text-sm text-[var(--semi-color-text-2)]">{t("Apple import lifetime")}</p>
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
          <Space>
            <Button disabled={busy} theme={mode === "paste" ? "solid" : "outline"} onClick={() => setMode("paste")}>{t("Manual input")}</Button>
            <Button disabled={busy} theme={mode === "file" ? "solid" : "outline"} onClick={() => setMode("file")}>{t("TXT file")}</Button>
          </Space>
          {mode === "paste" ? <TextArea aria-label={t("Apple account entries")} rows={8} className="font-mono" disabled={busy} value={text} onChange={setText} placeholder="email----password----https://example.com/code" /> :
            <input type="file" accept=".txt,text/plain" aria-label={t("TXT file")} disabled={busy} onChange={(event) => setFile(event.target.files?.[0] ?? null)} />}
          <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3 text-xs">
            <p>{t("Supported format")}</p>
            <pre className="overflow-x-auto py-2">{t("Apple import format")}</pre>
            <p>{t("Apple import accepts up to 100 accounts and a 1 MB TXT file.")}</p>
          </div>
        </>}
      </div>
    </Modal>
  );
}
