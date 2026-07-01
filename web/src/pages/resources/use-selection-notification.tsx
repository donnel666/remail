import { useEffect } from "react";
import { Button, Notification, Space, Typography } from "@douyinfe/semi-ui";

const { Text } = Typography;

interface UseSelectionNotificationOptions {
  selectedCount: number;
  onClear: () => void;
  t: (key: string, options?: Record<string, unknown>) => string;
}

export function useSelectionNotification({
  selectedCount,
  onClear,
  t,
}: UseSelectionNotificationOptions) {
  useEffect(() => {
    const noticeId = "resources-batch-actions";
    if (selectedCount > 0) {
      Notification.info({
        content: (
          <Space wrap>
            <Button onClick={onClear} size="small" theme="solid" type="tertiary">
              {t("Clear selection")}
            </Button>
            <Button size="small" theme="solid" type="primary">
              {t("Check")}
            </Button>
            <Button size="small" theme="solid" type="secondary">
              {t("Sell")}
            </Button>
            <Button size="small" theme="solid" type="danger">
              {t("Disable")}
            </Button>
          </Space>
        ),
        duration: 0,
        id: noticeId,
        position: "bottom",
        showClose: false,
        title: (
          <Space wrap>
            <span>{t("Batch action")}</span>
            <Text size="small" type="tertiary">
              {t("Selected resources", { count: selectedCount })}
            </Text>
          </Space>
        ),
      });
    } else {
      Notification.close(noticeId);
    }

    return () => {
      Notification.close(noticeId);
    };
  }, [onClear, selectedCount, t]);
}
