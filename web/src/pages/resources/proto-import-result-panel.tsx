import { useEffect, useRef, useState } from "react";
import { Button, Space, Table, Tag, Typography } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { getAdminProtoResourceImport, getAdminProtoResourceImportItems, getAdminProtoResourceImportFailures } from "@/lib/admin-proto-api";
import { getProtoResourceImport, getProtoResourceImportItems, getProtoResourceImportFailures, type ProtoImportResponse, type ProtoImportItemsResponse } from "@/lib/proto-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

export function ProtoImportResultPanel({ admin, importId, result, onProgress, poll = true }: {
  admin: boolean;
  importId: number;
  result: ProtoImportResponse | null;
  onProgress: (result: ProtoImportResponse) => void;
  poll?: boolean;
}) {
  const { t } = useTranslation();
  const [page, setPage] = useState(1);
  const [items, setItems] = useState<ProtoImportItemsResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const [downloading, setDownloading] = useState(false);
  const downloadRef = useRef<AbortController | null>(null);
  useEffect(() => () => downloadRef.current?.abort(), [importId]);
  const downloadFailures = async () => {
    if (downloadRef.current) return;
    const controller = new AbortController();
    downloadRef.current = controller;
    setDownloading(true);
    try {
      const blob = await (admin ? getAdminProtoResourceImportFailures : getProtoResourceImportFailures)(importId, controller.signal);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = "proto-import-failures.csv";
      link.click();
      URL.revokeObjectURL(url);
    } catch (reason) {
      if (!controller.signal.aborted) setError(getIamErrorMessage(t, reason, "Proto import failure download failed."));
    } finally {
      if (downloadRef.current === controller) downloadRef.current = null;
      setDownloading(false);
    }
  };
  useEffect(() => { setPage(1); setItems(null); }, [importId]);
  useEffect(() => {
    if (!poll) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refresh = async () => {
      try {
        const value = await (admin ? getAdminProtoResourceImport : getProtoResourceImport)(importId, controller.signal);
        if (controller.signal.aborted) return;
        onProgress(value);
        setError("");
        if (value.status === "processing") timer = setTimeout(() => void refresh(), 1500);
      } catch (reason) {
        if (!controller.signal.aborted) setError(getIamErrorMessage(t, reason, "Proto import progress load failed."));
      }
    };
    void refresh();
    return () => { controller.abort(); if (timer) clearTimeout(timer); };
  }, [admin, importId, onProgress, poll, retry, t]);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    void (admin ? getAdminProtoResourceImportItems : getProtoResourceImportItems)(importId, (page - 1) * 20, 20, controller.signal)
      .then((value) => {
        if (controller.signal.aborted) return;
        const lastPage = Math.max(1, Math.ceil(value.total / 20));
        if (page > lastPage) setPage(lastPage);
        else setItems(value);
      })
      .catch((reason) => { if (!controller.signal.aborted) setError(getIamErrorMessage(t, reason, "Proto import results load failed.")); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [admin, importId, page, result?.status, retry, t]);
  return <div className="rounded-xl border border-[var(--semi-color-border)] p-3">
    <Space className="mb-3" wrap>
      <Typography.Text strong>{t("Import result")} #{importId}</Typography.Text>
      {result ? <Tag color={result.status === "failed" ? "red" : result.status === "processing" ? "blue" : "green"}>
        {t(result.status === "failed" ? "Failed" : result.status === "processing" ? "Processing" : "Completed")}
      </Tag> : null}
      <Button size="small" onClick={() => setRetry((value) => value + 1)}>{t("Refresh")}</Button>
      {result?.failureAvailable ? <Button size="small" loading={downloading} onClick={() => void downloadFailures()}>{t("Download import failures")}</Button> : null}
    </Space>
    {result ? <div className="mb-2 text-xs text-[var(--semi-color-text-2)]">
      {t("Imported")}: {result.imported} · {t("Skipped")}: {result.skipped} · {t("Failed")}: {result.failed}
      {result.lastSafeError ? <div className="mt-1 text-[var(--semi-color-warning)]">{result.lastSafeError}</div> : null}
    </div> : null}
    {error ? <div role="alert" className="mb-2 text-sm text-[var(--semi-color-danger)]">{error}</div> : null}
    <Table
      rowKey="line"
      size="small"
      loading={loading}
      dataSource={items?.items ?? []}
      columns={[
        { title: t("Line"), dataIndex: "line", width: 65 },
        { title: t("Result"), dataIndex: "outcome", width: 110, render: (value: string) => t(value === "imported" ? "Imported" : value === "restored" ? "Recovered" : value === "skipped" ? "Skipped" : "Failed") },
        { title: t("Resource ID"), dataIndex: "resourceId", width: 110 },
        { title: t("Reason"), dataIndex: "lastSafeError", render: (value?: string) => value || "-" },
      ]}
      pagination={{ currentPage: page, pageSize: 20, total: items?.total ?? 0, showSizeChanger: false, onPageChange: setPage }}
    />
  </div>;
}
