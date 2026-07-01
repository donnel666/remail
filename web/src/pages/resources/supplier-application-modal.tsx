import { useState } from "react";
import { Button, Modal, Space, TextArea, Toast } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";
import { submitSupplierApplication } from "@/lib/resources-api";

interface SupplierApplicationModalProps {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  onSuccess: () => void;
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
      await submitSupplierApplication({ reason: trimmedReason });
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
