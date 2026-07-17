import { useEffect, useMemo, useState } from "react";
import { Modal, Spin, Tag, Toast } from "@douyinfe/semi-ui";
import { AtSign, RefreshCcw, ScanSearch, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  createAdminMicrosoftExplicitAlias,
  listAdminMicrosoftTasks,
  refreshAdminMicrosoftToken,
  scanAdminMicrosoftProjects,
  validateAdminMicrosoftResource,
} from "@/lib/admin-microsoft-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

import { TASK_STATUS_META, formatTime } from "./microsoft-meta";
import type {
  AdminMicrosoftAsyncTask,
  AdminMicrosoftResourceItem,
  AdminMicrosoftTaskStatus,
} from "./admin-microsoft-types";

type MaintenanceAction = "validate" | "alias" | "history" | "token";
type MaintenanceStatus = AdminMicrosoftTaskStatus | "idle" | "unavailable";

function validationStatus(target: AdminMicrosoftResourceItem): MaintenanceStatus {
  switch (target.status) {
    case "pending":
      return "queued";
    case "validating":
      return "running";
    case "normal":
      return "succeeded";
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

export function MicrosoftMaintenanceModal({
  onCancel,
  onCompleted,
  target,
}: {
  onCancel: () => void;
  onCompleted: () => void | Promise<void>;
  target: AdminMicrosoftResourceItem | null;
}) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState<MaintenanceAction>("validate");
  const [tasks, setTasks] = useState<AdminMicrosoftAsyncTask[]>([]);
  const [loadingTasks, setLoadingTasks] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!target) return;
    setSelected(target.status === "normal" ? "validate" : target.status === "disabled" ? "token" : "validate");
    setTasks([]);
    const controller = new AbortController();
    setLoadingTasks(true);
    void listAdminMicrosoftTasks(target.id, 0, 100, controller.signal)
      .then((response) => {
        if (!controller.signal.aborted) setTasks(response.items);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft task load failed."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoadingTasks(false);
      });
    return () => controller.abort();
  }, [t, target]);

  const latest = useMemo(() => {
    const result = new Map<string, AdminMicrosoftAsyncTask>();
    for (const task of tasks) {
      if (!result.has(task.kind)) result.set(task.kind, task);
    }
    return result;
  }, [tasks]);

  if (!target) return null;

  const tokenConfigured = target.tokenHealth !== "missing";
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
      action: validateAdminMicrosoftResource,
      description: "Re-run Microsoft resource health validation and update the resource result.",
      disabled: target.status === "disabled" || target.status === "deleted",
      disabledReason: "Enable the resource before validation.",
      icon: ShieldCheck,
      key: "validate",
      label: "Validate resource",
      status: validationStatus(target),
      success: "Resource validation submitted.",
      updatedAt: target.updatedAt,
    },
    {
      action: createAdminMicrosoftExplicitAlias,
      description: "Wake the existing alias schedule without bypassing quota or reconciliation.",
      disabled: target.status !== "normal",
      disabledReason: "Alias creation requires a normal resource.",
      icon: AtSign,
      key: "alias",
      label: "Create alias",
      status: latest.get("alias")?.status ?? "idle",
      success: "Explicit alias creation submitted.",
      updatedAt: latest.get("alias")?.updatedAt,
    },
    {
      action: scanAdminMicrosoftProjects,
      description: "Scan full Inbox and Junk history and restore existing project relationships.",
      disabled: target.status !== "normal" || !tokenConfigured,
      disabledReason: "Project scanning requires a normal resource with OAuth credentials.",
      icon: ScanSearch,
      key: "history",
      label: "Scan projects",
      status: latest.get("history")?.status ?? "idle",
      success: "Project history scan submitted.",
      updatedAt: latest.get("history")?.updatedAt,
    },
    {
      action: refreshAdminMicrosoftToken,
      description: "Refresh the current Microsoft RT through the existing fenced task.",
      disabled: target.status === "deleted" || !tokenConfigured,
      disabledReason: "RT refresh requires configured OAuth credentials.",
      icon: RefreshCcw,
      key: "token",
      label: "Update RT",
      status: latest.get("token")?.status ?? "idle",
      success: "Token refresh submitted.",
      updatedAt: latest.get("token")?.updatedAt,
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
      Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
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
      title={t("Microsoft resource maintenance")}
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
