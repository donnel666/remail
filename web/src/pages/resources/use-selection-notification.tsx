import { useEffect } from "react";
import { Button, Notification, Space, Typography } from "@douyinfe/semi-ui";

const { Text } = Typography;
const selectionNoticeId = "resources-batch-actions";

interface UseSelectionNotificationOptions {
  onCheck?: () => void;
  selectedCount: number;
  onClear: () => void;
  onDelete?: () => void;
  onSell?: () => void;
  deleteLoading?: boolean;
  selectionDescriptionKey?: string;
  sellLoading?: boolean;
  t: (key: string, options?: Record<string, unknown>) => string;
}

export function useSelectionNotification({
  onCheck,
  selectedCount,
  onClear,
  onDelete,
  onSell,
  deleteLoading = false,
  selectionDescriptionKey = "Selected resources",
  sellLoading = false,
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
                onClick={onCheck}
                size="small"
                theme="solid"
                type="primary"
              >
                {t("Check")}
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
                {t("Sell")}
              </Button>
            ) : null}
            {onDelete ? (
              <Button
                loading={deleteLoading}
                onClick={onDelete}
                size="small"
                theme="solid"
                type="danger"
              >
                {t("Delete")}
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
    onCheck,
    onClear,
    onDelete,
    onSell,
    selectionDescriptionKey,
    selectedCount,
    sellLoading,
    t,
  ]);

  useEffect(() => {
    return () => {
      Notification.close(selectionNoticeId);
    };
  }, []);
}
