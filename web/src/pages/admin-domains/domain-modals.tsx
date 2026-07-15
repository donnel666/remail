import { useEffect, useState } from "react";
import { Input, Modal, Select, Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import type {
  AdminDomainItem,
  AdminDomainOwner,
  AdminDomainPurpose,
  AdminDomainStatus,
  AdminMailServer,
} from "./admin-domains-mock";

export type DomainEditorMode = "import" | "edit";

export interface DomainDraft {
  domain: string;
  ownerId?: number;
  purpose: AdminDomainPurpose;
  mailServerId?: number;
  status: Extract<AdminDomainStatus, "normal" | "abnormal" | "disabled">;
}

const MX_TARGET = "mx.aishop6.com";

export function DomainFormModal({
  mode,
  target,
  owners,
  mailServers,
  onCancel,
  onSubmit,
  visible,
}: {
  mode: DomainEditorMode;
  target: AdminDomainItem | null;
  owners: AdminDomainOwner[];
  mailServers: AdminMailServer[];
  onCancel: () => void;
  onSubmit: (draft: DomainDraft) => Promise<void>;
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<DomainDraft>({
    domain: "",
    ownerId: undefined,
    purpose: "not_sale",
    mailServerId: undefined,
    status: "abnormal",
  });
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!visible) return;
    if (mode === "edit" && target) {
      setDraft({
        domain: target.domain,
        ownerId: target.ownerId,
        purpose: target.purpose,
        mailServerId: target.mailServerId,
        status:
          target.status === "deleted"
            ? "abnormal"
            : (target.status as DomainDraft["status"]),
      });
    } else {
      setDraft({
        domain: "",
        ownerId: owners[0]?.id,
        purpose: "not_sale",
        mailServerId: mailServers[0]?.id,
        status: "abnormal",
      });
    }
  }, [mailServers, mode, owners, target, visible]);

  const setField = <K extends keyof DomainDraft>(
    key: K,
    value: DomainDraft[K]
  ) => {
    setDraft((previous) => ({ ...previous, [key]: value }));
  };

  const submit = async () => {
    if (mode === "import" && !draft.domain.trim()) {
      Toast.error(t("Please enter a domain."));
      return;
    }
    if (!draft.ownerId) {
      Toast.error(t("Please select an owner."));
      return;
    }
    setSubmitting(true);
    try {
      await onSubmit(draft);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      confirmLoading={submitting}
      okText={mode === "edit" ? t("Save") : t("Import")}
      onCancel={onCancel}
      onOk={() => void submit()}
      title={
        mode === "edit" && target
          ? `${t("Edit domain")} #${target.id}`
          : t("Import Domain Email")
      }
      visible={visible}
      width={520}
    >
      <div className="space-y-3">
        {mode === "import" ? (
          <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
            <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
              {t("MX Record")}
            </div>
            <div className="font-mono text-sm text-[var(--semi-color-primary)]">
              {MX_TARGET}
            </div>
            <div className="mt-1 text-xs text-[var(--semi-color-text-2)]">
              {t(
                "Set your domain's MX record to the address above, then enter your domain below"
              )}
            </div>
          </div>
        ) : null}

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Domain")} *
          </span>
          <Input
            disabled={mode === "edit"}
            onChange={(value) => setField("domain", String(value))}
            placeholder="example.com"
            value={draft.domain}
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Owner")} *
          </span>
          <Select
            filter
            onChange={(value) => setField("ownerId", Number(value))}
            optionList={owners.map((owner) => ({
              label: `${owner.email} · ${owner.nickname} · #${owner.id}`,
              value: owner.id,
            }))}
            placeholder={t("Search user by email, nickname or ID")}
            style={{ width: "100%" }}
            value={draft.ownerId}
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Purpose")}
          </span>
          <Select
            onChange={(value) =>
              setField("purpose", String(value) as AdminDomainPurpose)
            }
            style={{ width: "100%" }}
            value={draft.purpose}
          >
            <Select.Option value="not_sale">{t("Not for sale")}</Select.Option>
            <Select.Option value="sale">{t("Sale")}</Select.Option>
            <Select.Option value="binding">{t("Binding")}</Select.Option>
          </Select>
        </label>
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Mail server")}
          </span>
          <Select
            onChange={(value) => setField("mailServerId", Number(value))}
            optionList={mailServers.map((server) => ({
              label: `${server.name} · ${server.mxRecord}`,
              value: server.id,
            }))}
            style={{ width: "100%" }}
            value={draft.mailServerId}
          />
        </label>
        {mode === "edit" ? (
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
              {t("Status")}
            </span>
            <Select
              onChange={(value) =>
                setField("status", String(value) as DomainDraft["status"])
              }
              style={{ width: "100%" }}
              value={draft.status}
            >
              <Select.Option value="normal">{t("Normal")}</Select.Option>
              <Select.Option value="abnormal">{t("Abnormal")}</Select.Option>
              <Select.Option value="disabled">{t("Disabled")}</Select.Option>
            </Select>
          </label>
        ) : null}
      </div>
    </Modal>
  );
}
