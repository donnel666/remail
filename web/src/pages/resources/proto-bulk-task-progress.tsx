import { useEffect, useRef, useState } from "react";
import { Button, Space, Tag } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { getAdminProtoBulkTask } from "@/lib/admin-proto-api";
import { getProtoBulkTask } from "@/lib/proto-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import type { AdminProtoBulkResponse } from "../admin-proto/admin-proto-types";

export function ProtoBulkTaskProgress({ admin = false, taskId, onCompleted, onClose }: {
  admin?: boolean; taskId: string | null; onCompleted: () => void | Promise<void>; onClose: () => void;
}) {
  const { t } = useTranslation();
  const [task, setTask] = useState<AdminProtoBulkResponse | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const completedRef = useRef(onCompleted);
  completedRef.current = onCompleted;
  useEffect(() => {
    if (!taskId) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    setTask(null);
    setError("");
    const poll = async () => {
      try {
        const result = await (admin ? getAdminProtoBulkTask : getProtoBulkTask)(taskId, controller.signal);
        if (controller.signal.aborted) return;
        setTask(result);
        if (result.status === "queued" || result.status === "running") timer = setTimeout(() => void poll(), 1500);
        else await completedRef.current();
      } catch (reason) {
        if (!controller.signal.aborted) setError(getIamErrorMessage(t, reason, "Proto batch progress load failed."));
      }
    };
    void poll();
    return () => { controller.abort(); if (timer) clearTimeout(timer); };
  }, [admin, retry, t, taskId]);
  if (!taskId) return null;
  return <div role="status" className="mb-4 rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
    <Space wrap>
      <span className="text-sm">{t("Batch task")} <code>{taskId}</code></span>
      {task ? <Tag color={task.status === "failed" ? "red" : task.status === "succeeded" ? "green" : "blue"}>
        {t(task.status === "succeeded" ? "Succeeded" : task.status === "failed" ? "Failed" : task.status === "running" ? "Running" : "Queued")}
      </Tag> : null}
      <Button size="small" onClick={() => setRetry((value) => value + 1)}>{t("Refresh")}</Button>
      <Button size="small" theme="borderless" onClick={onClose}>{t("Close")}</Button>
    </Space>
    {task ? <div className="mt-2 text-xs text-[var(--semi-color-text-2)]">
      {t("Processed")}: {task.processed ?? 0}/{task.requested} · {t("Succeeded")}: {task.affected} · {t("Skipped")}: {task.skipped}
      {task.reasonCounts?.length ? <div>{task.reasonCounts.map((item) => item.reason + ": " + item.count).join(" · ")}</div> : null}
    </div> : null}
    {error ? <div role="alert" className="mt-2 text-sm text-[var(--semi-color-danger)]">{error}</div> : null}
  </div>;
}
