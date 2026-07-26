import { useState } from "react";
import { Button, Modal, Space, TextArea, Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";

import { createTicket } from "../tickets/tickets-api";

interface SupplierApplicationModalProps {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  onSuccess: () => void;
}

export function hasSupplierRole(role?: string | null) {
  return role === "supplier" || role === "admin" || role === "super_admin";
}

export function createSupplierApplicationTicket(reason: string) {
  return createTicket({
    ticketType: "general",
    title: "供应商申请",
    firstMessage: reason,
  });
}

export async function ensureSupplierRole(
  currentRole: string | null | undefined,
  refreshCurrentUser: () => Promise<{ role?: string | null } | null>,
  openApplication: () => void
) {
  if (hasSupplierRole(currentRole)) return true;
  if (hasSupplierRole((await refreshCurrentUser())?.role)) return true;
  openApplication();
  return false;
}

export function SupplierApplicationModal({
  open,
  onOpenChange,
  onSuccess,
}: SupplierApplicationModalProps) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);

  const close = () => {
    setReason("");
    setBusy(false);
    onOpenChange(false);
  };

  const submit = async () => {
    const trimmedReason = reason.trim();
    if (!trimmedReason) {
      Toast.warning(t("Please enter supplier application reason."));
      return;
    }

    setBusy(true);
    try {
      await createSupplierApplicationTicket(trimmedReason);
      Toast.success(t("Supplier application submitted."));
      close();
      onSuccess();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(t, error, "Supplier application failed.")
      );
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
          <Button loading={busy} onClick={submit} type="primary">
            {busy ? t("Submitting") : t("Submit")}
          </Button>
        </Space>
      }
      onCancel={close}
      title={t("Apply supplier")}
      visible={open}
    >
      <TextArea
        autosize={{ minRows: 5, maxRows: 8 }}
        maxCount={1000}
        onChange={(value) => setReason(String(value))}
        placeholder={t("Supplier application reason placeholder")}
        showClear
        value={reason}
      />
    </Modal>
  );
}
