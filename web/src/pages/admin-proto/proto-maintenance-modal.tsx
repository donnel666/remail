import { useEffect, useMemo, useState } from "react";
import { Modal, Spin, Tag, Toast } from "@douyinfe/semi-ui";
import { ScanSearch, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  listAdminProtoTasks,
  scanAdminProtoProjects,
  validateAdminProtoResource,
} from "@/lib/admin-proto-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { IamApiError } from "@/lib/api-client";

import { TASK_STATUS_META, formatTime } from "./proto-meta";
import type {
  AdminProtoAsyncTask,
  AdminProtoResourceItem,
  AdminProtoTaskStatus,
} from "./admin-proto-types";

type MaintenanceAction = "validate" | "history";
type MaintenanceStatus = AdminProtoTaskStatus | "idle" | "unavailable";

function validationStatus(target: AdminProtoResourceItem): MaintenanceStatus {
  switch (target.status) {
    case "pending":
      return "queued";
    case "validating":
      return "running";
    case "identifying":
      return "succeeded";
    case "normal":
      return "succeeded";
    case "validation_failed":
    case "abnormal":
      return "failed";
    default:
      return "unavailable";
  }
}

function statusTag(status: MaintenanceStatus, t: ReturnType<typeof useTranslation>["t"]) {
  if (status === "idle" || status === "unavailable") {
    return (
      <Tag color="grey" shape="circle" size="small">
        {t(status === "idle" ? "Idle" : "Unavailable")}
      </Tag>
    );
  }
  const meta = TASK_STATUS_META[status];
  return (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
}

export function ProtoMaintenanceModal({
  onCancel,
  onCompleted,
  target,
}: {
  onCancel: () => void;
  onCompleted: () => void | Promise<void>;
  target: AdminProtoResourceItem | null;
}) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState<MaintenanceAction>("validate");
  const [tasks, setTasks] = useState<AdminProtoAsyncTask[]>([]);
  const [loadingTasks, setLoadingTasks] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!target) return;
    setSelected(target.status === "identifying" ? "history" : "validate");
    setTasks([]);
    const controller = new AbortController();
    setLoadingTasks(true);
    void listAdminProtoTasks(target.id, 0, 100, controller.signal)
      .then((response) => {
        if (!controller.signal.aborted) setTasks(response.items);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "Proto task load failed."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoadingTasks(false);
      });
    return () => controller.abort();
  }, [t, target]);

  const latest = useMemo(() => {
    const result = new Map<string, AdminProtoAsyncTask>();
    for (const task of tasks) {
      if (!result.has(task.kind)) result.set(task.kind, task);
    }
    return result;
  }, [tasks]);

  if (!target) return null;

  const actions: Array<{
    action: (resourceId: number) => Promise<unknown>;
    description: string;
    disabled: boolean;
    disabledReason?: string;
    icon: typeof ShieldCheck;
    key: MaintenanceAction;
    label: string;
    status: MaintenanceStatus;
    success: string;
    updatedAt?: string;
  }> = [
    {
      action: (id) => validateAdminProtoResource(id, target.version),
      description: "Sign in to Proto, securely save mailbox keys, then identify historical project usage.",
      disabled: target.status === "disabled" || target.status === "deleted",
      disabledReason: "Enable the resource before validation.",
      icon: ShieldCheck,
      key: "validate",
      label: "Validate resource",
      status: latest.get("validation")?.status ?? validationStatus(target),
      success: "Resource validation submitted.",
      updatedAt: target.updatedAt,
    },
    {
      action: (id) => scanAdminProtoProjects(id, target.version),
      description: "Read the complete inbox and spam history with saved keys and record confirmed project usage.",
      disabled: target.status !== "normal" && target.status !== "identifying",
      disabledReason: "Project scanning requires a validated resource.",
      icon: ScanSearch,
      key: "history",
      label: "Scan projects",
      status: latest.get("history")?.status ?? "idle",
      success: "Project history scan submitted.",
      updatedAt: latest.get("history")?.updatedAt,
    },
  ];
  const selectedAction = actions.find((item) => item.key === selected) ?? actions[0];

  const submit = async () => {
    if (!selectedAction || selectedAction.disabled) return;
    setSubmitting(true);
    try {
      await selectedAction.action(target.id);
      Toast.success(t(selectedAction.success));
      await onCompleted();
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Proto resource operation failed."));
      if (error instanceof IamApiError && error.code === "resource_version_conflict") { await onCompleted(); onCancel(); }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      okButtonProps={{ disabled: selectedAction.disabled }}
      okText={t("Submit maintenance task")}
      onCancel={onCancel}
      onOk={() => void submit()}
      title={t("Proto resource maintenance")}
      visible
      width={680}
    >
      <div className="space-y-4 py-1">
        <div className="rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
          <div className="text-xs text-[var(--semi-color-text-2)]">{t("Resource")}</div>
          <div className="mt-1 break-all font-mono text-sm font-medium text-[var(--semi-color-text-0)]">
            {target.emailAddress}
          </div>
        </div>

        <div className="flex items-center justify-between gap-3">
          <div className="text-sm text-[var(--semi-color-text-1)]">
            {t("Choose one maintenance operation. Each operation keeps its existing backend task lifecycle.")}
          </div>
          {loadingTasks ? <Spin size="small" /> : null}
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          {actions.map((item) => {
            const Icon = item.icon;
            const active = selected === item.key;
            return (
              <button
                aria-pressed={active}
                className={`min-h-32 rounded-xl border p-4 text-left transition-colors ${
                  active
                    ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)]"
                    : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]"
                } ${item.disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer"}`}
                disabled={item.disabled || submitting}
                key={item.key}
                onClick={() => setSelected(item.key)}
                type="button"
              >
                <div className="flex items-start gap-3">
                  <span className="rounded-lg bg-[var(--semi-color-fill-0)] p-2 text-[var(--semi-color-primary)]">
                    <Icon aria-hidden size={20} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-semibold text-[var(--semi-color-text-0)]">
                        {t(item.label)}
                      </span>
                      {statusTag(item.status, t)}
                    </span>
                    <span className="mt-1.5 block text-xs leading-5 text-[var(--semi-color-text-2)]">
                      {t(item.disabled ? item.disabledReason ?? item.description : item.description)}
                    </span>
                    {item.updatedAt ? (
                      <span className="mt-2 block text-xs text-[var(--semi-color-text-3)]">
                        {t("Last updated")}: {formatTime(item.updatedAt)}
                      </span>
                    ) : null}
                  </span>
                </div>
              </button>
            );
          })}
        </div>
      </div>
    </Modal>
  );
}
