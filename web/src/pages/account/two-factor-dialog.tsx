import { Button, Input, Modal, Typography } from "@douyinfe/semi-ui";
import { QRCodeSVG } from "qrcode.react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

const { Text } = Typography;

export interface TwoFactorSetup {
  otpauthUri: string;
  expiresAt?: string;
}

interface TwoFactorDialogProps {
  error: string;
  onCancel: () => void;
  onConfirm: (code: string) => void;
  open: boolean;
  setup: TwoFactorSetup | null;
  submitting: boolean;
  enabled: boolean;
}

export function TwoFactorDialog({
  error,
  onCancel,
  onConfirm,
  open,
  setup,
  submitting,
  enabled,
}: TwoFactorDialogProps) {
  const { t } = useTranslation();
  const [code, setCode] = useState("");
  const setupKey = setup?.otpauthUri ? new URL(setup.otpauthUri).searchParams.get("secret") || "" : "";

  useEffect(() => {
    if (!open) setCode("");
  }, [open]);

  const close = () => {
    if (submitting) return;
    setCode("");
    onCancel();
  };

  return (
    <Modal
      centered
      confirmLoading={submitting}
      onCancel={close}
      footer={null}
      size="small"
      title={t(enabled ? "Disable two-factor authentication" : "Enable two-factor authentication")}
      visible={open}
    >
      <div className="space-y-4">
        {error ? <div className="account-form-error" role="alert">{error}</div> : null}
        {enabled ? (
          <>
            <Text type="tertiary">{t("Enter your current authenticator code to disable two-factor authentication. An administrator can help if you lose access to your authenticator.")}</Text>
            <Input
              aria-label={t("Authenticator code")}
              autoComplete="one-time-code"
              inputMode="numeric"
              maxLength={6}
              onChange={(value) => setCode(String(value).replace(/\D/g, "").slice(0, 6))}
              placeholder={t("6-digit code")}
              size="large"
              value={code}
            />
          </>
        ) : (
          <>
            <Text type="tertiary">{t("Scan this QR code with your authenticator app, then enter the generated code to confirm.")}</Text>
            {setup?.otpauthUri ? (
              <div className="flex justify-center rounded-lg border border-[var(--divider)] bg-white p-4">
                <QRCodeSVG aria-label={t("Authenticator setup QR code")} size={220} value={setup.otpauthUri} />
              </div>
            ) : null}
            <Text type="tertiary">{t("If you cannot scan the code, use the manual setup option in your authenticator app.")}</Text>
            <Input
              aria-label={t("Authenticator setup key")}
              readOnly
              value={setupKey}
            />
            <Input
              aria-label={t("Authenticator code")}
              autoComplete="one-time-code"
              inputMode="numeric"
              maxLength={6}
              onChange={(value) => setCode(String(value).replace(/\D/g, "").slice(0, 6))}
              placeholder={t("6-digit code")}
              size="large"
              value={code}
            />
          </>
        )}
        <Button
          className="w-full"
          disabled={submitting || code.length !== 6}
          loading={submitting}
          onClick={() => onConfirm(code)}
          theme="solid"
          type="primary"
        >
          {t(enabled ? "Disable 2FA" : "Confirm and enable 2FA")}
        </Button>
      </div>
    </Modal>
  );
}
