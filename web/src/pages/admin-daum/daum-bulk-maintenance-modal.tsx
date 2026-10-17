import { useEffect, useState } from "react";
import { Modal, Tag, Toast } from "@douyinfe/semi-ui";
import { ScanSearch, ShieldCheck } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  maintainAdminDaumResourcesByFilter,
  maintainAdminDaumResourcesByIds,
} from "@/lib/admin-daum-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

import type {
  AdminDaumListFilter,
  AdminDaumMaintenanceAction,
} from "./admin-daum-types";

export type DaumBulkMaintenanceTarget =
  | { count: number; mode: "ids"; resourceIds: number[] }
  | { count: number; filter: AdminDaumListFilter; mode: "filter" };

export function DaumBulkMaintenanceModal({
  onCancel,
  onCompleted,
  target,
}: {
  onCancel: () => void;
  onCompleted: (taskId?: string) => void | Promise<void>;
  target: DaumBulkMaintenanceTarget | null;
}) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState<AdminDaumMaintenanceAction>("validate");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (target) setSelected("validate");
  }, [target]);

  if (!target) return null;

  const actions: Array<{
    description: string;
    icon: typeof ShieldCheck;
    key: AdminDaumMaintenanceAction;
    label: string;
    success: string;
  }> = [
    {
      description: "Queue validation for every eligible resource in this batch.",
      icon: ShieldCheck,
      key: "validate",
      label: "Validate resource",
      success: "Resource validation batch submitted.",
    },
    {
      description: "Scan Daum mail history and record project usage for each complete recipient address. IMAP is required for complete history.",
      icon: ScanSearch,
      key: "history",
      label: "Scan projects",
      success: "Project scan batch submitted.",
    },
  ];
  const selectedAction = actions.find((item) => item.key === selected) ?? actions[0];

  const submit = async () => {
    if (!selectedAction) return;
    setSubmitting(true);
    try {
      const response =
        target.mode === "ids"
          ? await maintainAdminDaumResourcesByIds(selectedAction.key, target.resourceIds)
          : await maintainAdminDaumResourcesByFilter(selectedAction.key, target.filter);
      Toast.success(t(selectedAction.success, { count: response.requested }));
      await onCompleted(response.taskId);
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Daum resource operation failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      okText={t("Submit maintenance task")}
      onCancel={onCancel}
      onOk={() => void submit()}
      title={t("Daum resource maintenance")}
      visible
      width={680}
    >
      <div className="space-y-4 py-1">
        <div className="flex items-center justify-between gap-3 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
          <div>
            <div className="text-xs text-[var(--semi-color-text-2)]">{t("Scope")}</div>
            <div className="mt-1 text-sm font-medium text-[var(--semi-color-text-0)]">
              {t(target.mode === "ids" ? "Selected Daum resources" : "Matching resources", {
                count: target.count,
              })}
            </div>
          </div>
          <Tag color="blue" shape="circle">
            {target.count}
          </Tag>
        </div>

        <div className="text-sm leading-6 text-[var(--semi-color-text-1)]">
          {t("Choose one maintenance operation. Ineligible resources will be skipped and counted by the server.")}
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          {actions.map((item) => {
            const Icon = item.icon;
            const active = selected === item.key;
            return (
              <button
                aria-pressed={active}
                className={`min-h-32 cursor-pointer rounded-xl border p-4 text-left transition-colors ${
                  active
                    ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)]"
                    : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]"
                }`}
                disabled={submitting}
                key={item.key}
                onClick={() => setSelected(item.key)}
                type="button"
              >
                <div className="flex items-start gap-3">
                  <span className="rounded-lg bg-[var(--semi-color-fill-0)] p-2 text-[var(--semi-color-primary)]">
                    <Icon aria-hidden size={20} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="font-semibold text-[var(--semi-color-text-0)]">
                      {t(item.label)}
                    </span>
                    <span className="mt-1.5 block text-xs leading-5 text-[var(--semi-color-text-2)]">
                      {t(item.description)}
                    </span>
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
