import { IconLock } from "@douyinfe/semi-icons";
import { Input, Modal, Typography } from "@douyinfe/semi-ui";
import { useTranslation } from "react-i18next";

const { Text } = Typography;

interface ChangePasswordDialogProps {
  confirmPassword: string;
  error: string;
  newPassword: string;
  oldPassword: string;
  onCancel: () => void;
  onConfirm: () => void;
  onConfirmPasswordChange: (value: string) => void;
  onNewPasswordChange: (value: string) => void;
  onOldPasswordChange: (value: string) => void;
  open: boolean;
  submitting: boolean;
}

export function ChangePasswordDialog({
  confirmPassword,
  error,
  newPassword,
  oldPassword,
  onCancel,
  onConfirm,
  onConfirmPasswordChange,
  onNewPasswordChange,
  onOldPasswordChange,
  open,
  submitting,
}: ChangePasswordDialogProps) {
  const { t } = useTranslation();

  return (
    <Modal
      centered
      className="account-password-modal"
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={onConfirm}
      size="small"
      title={
        <div className="account-modal-title">
          <IconLock />
          {t("Change password")}
        </div>
      }
      visible={open}
    >
      <div className="account-password-modal-body">
        {error ? <div className="account-form-error">{error}</div> : null}

        <div>
          <Text strong>{t("Current password")}</Text>
          <Input
            autoComplete="current-password"
            className="!rounded-lg mt-2"
            mode="password"
            onChange={(value) => onOldPasswordChange(String(value))}
            placeholder={t("Current password")}
            prefix={<IconLock />}
            size="large"
            value={oldPassword}
          />
        </div>

        <div>
          <Text strong>{t("New password")}</Text>
          <Input
            autoComplete="new-password"
            className="!rounded-lg mt-2"
            mode="password"
            onChange={(value) => onNewPasswordChange(String(value))}
            placeholder={t("New password")}
            prefix={<IconLock />}
            size="large"
            value={newPassword}
          />
        </div>

        <div>
          <Text strong>{t("Confirm password")}</Text>
          <Input
            autoComplete="new-password"
            className="!rounded-lg mt-2"
            mode="password"
            onChange={(value) => onConfirmPasswordChange(String(value))}
            placeholder={t("Confirm password")}
            prefix={<IconLock />}
            size="large"
            value={confirmPassword}
          />
        </div>
      </div>
    </Modal>
  );
}
