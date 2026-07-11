import { useEffect } from "react";
import { Button, Notification, Space, Typography } from "@douyinfe/semi-ui";

const { Text } = Typography;
const selectionNoticeId = "resources-batch-actions";

interface SelectionExtraAction {
  key: string;
  labelKey: string;
  onClick: () => void;
  loading?: boolean;
  type?: "primary" | "secondary" | "tertiary" | "warning" | "danger";
}

interface UseSelectionNotificationOptions {
  onCheck?: () => void;
  selectedCount: number;
  checkLoading?: boolean;
  checkLabelKey?: string;
  onClear: () => void;
  onDelete?: () => void;
  onSell?: () => void;
  deleteLoading?: boolean;
  deleteLabelKey?: string;
  extraActions?: SelectionExtraAction[];
  selectionDescriptionKey?: string;
  sellLoading?: boolean;
  sellLabelKey?: string;
  t: (key: string, options?: Record<string, unknown>) => string;
}

export function useSelectionNotification({
  onCheck,
  selectedCount,
  checkLoading = false,
  checkLabelKey = "Check",
  onClear,
  onDelete,
  onSell,
  deleteLoading = false,
  deleteLabelKey = "Delete",
  extraActions,
  selectionDescriptionKey = "Selected resources",
  sellLoading = false,
  sellLabelKey = "Sell",
  t,
}: UseSelectionNotificationOptions) {
  useEffect(() => {
    if (selectedCount > 0) {
      Notification.info({
        content: (
          <Space wrap>
            <Button onClick={onClear} size="small" theme="solid" type="tertiary">
              {t("Clear selection")}
            </Button>
            {onCheck ? (
              <Button
                loading={checkLoading}
                onClick={onCheck}
                size="small"
                theme="solid"
                type="primary"
              >
                {t(checkLabelKey)}
              </Button>
            ) : null}
            {onSell ? (
              <Button
                loading={sellLoading}
                onClick={onSell}
                size="small"
                theme="solid"
                type="secondary"
              >
                {t(sellLabelKey)}
              </Button>
            ) : null}
            {extraActions?.map((action) => (
              <Button
                key={action.key}
                loading={action.loading}
                onClick={action.onClick}
                size="small"
                theme="solid"
                type={action.type ?? "tertiary"}
              >
                {t(action.labelKey)}
              </Button>
            ))}
            {onDelete ? (
              <Button
                loading={deleteLoading}
                onClick={onDelete}
                size="small"
                theme="solid"
                type="danger"
              >
                {t(deleteLabelKey)}
              </Button>
            ) : null}
          </Space>
        ),
        duration: 0,
        id: selectionNoticeId,
        position: "bottom",
        showClose: false,
        title: (
          <Space wrap>
            <span>{t("Batch action")}</span>
            <Text size="small" type="tertiary">
              {t(selectionDescriptionKey, { count: selectedCount })}
            </Text>
          </Space>
        ),
      });
    } else {
      Notification.close(selectionNoticeId);
    }
  }, [
    deleteLoading,
    checkLoading,
    checkLabelKey,
    deleteLabelKey,
    extraActions,
    onCheck,
    onClear,
    onDelete,
    onSell,
    selectionDescriptionKey,
    selectedCount,
    sellLoading,
    sellLabelKey,
    t,
  ]);

  useEffect(() => {
    return () => {
      Notification.close(selectionNoticeId);
    };
  }, []);
}
