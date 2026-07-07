import { useState } from "react";
import {
  Button,
  Input,
  Modal,
  Space,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { createDomainResource } from "@/lib/resources-api";

const { Text } = Typography;

interface ImportDomainModalProps {
  open: boolean;
  onOpenChange: (value: boolean) => void;
  onSuccess: () => void | Promise<void>;
}

/** MX record target the user needs to configure */
const MX_TARGET = "mx.aishop6.com";

export function ImportDomainModal({
  open,
  onOpenChange,
  onSuccess,
}: ImportDomainModalProps) {
  const { t } = useTranslation();
  const [domain, setDomain] = useState("");
  const [busy, setBusy] = useState(false);

  const reset = () => {
    setDomain("");
    setBusy(false);
  };

  const close = () => {
    reset();
    onOpenChange(false);
  };

  const handleImport = async () => {
    const domainName = domain.trim().toLowerCase().replace(/\.$/, "");
    if (!domainName) return;
    setBusy(true);
    try {
      await createDomainResource({ domain: domainName });
      Toast.success(t("Domain submitted for verification"));
      close();
      await onSuccess();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Domain import failed."));
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
          <Button
            disabled={!domain.trim()}
            loading={busy}
            onClick={handleImport}
            type="primary"
          >
            {busy ? t("Submitting") : t("Import")}
          </Button>
        </Space>
      }
      onCancel={close}
      title={t("Import Domain Email")}
      visible={open}
      width={520}
    >
      <div className="space-y-4">
        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-4">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
            {t("MX Record")}
          </div>
          <div className="flex items-center gap-2">
            <Text
              copyable={createCopyableConfig(MX_TARGET, t("Copied"))}
              className="rounded bg-[var(--semi-color-fill-1)] px-2 py-1 font-mono text-sm text-[var(--semi-color-primary)]"
            >
              {MX_TARGET}
            </Text>
          </div>
          <Text size="small" type="tertiary" className="mt-2 block">
            {t(
              "Set your domain's MX record to the address above, then enter your domain below"
            )}
          </Text>
        </div>

        <div className="space-y-2">
          <div className="text-xs font-medium text-[var(--semi-color-text-2)]">
            {t("Your domain name")}
          </div>
          <Input
            onChange={(value) => setDomain(value)}
            placeholder="example.com"
            size="large"
            value={domain}
          />
        </div>
      </div>
    </Modal>
  );
}
