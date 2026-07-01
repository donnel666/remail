import { useEffect } from "react";
import { Button, Notification, Space, Typography } from "@douyinfe/semi-ui";

const { Text } = Typography;

interface UseSelectionNotificationOptions {
  onCheck?: () => void;
  selectedCount: number;
  onClear: () => void;
  onSell?: () => void;
  sellLoading?: boolean;
  t: (key: string, options?: Record<string, unknown>) => string;
}

export function useSelectionNotification({
  onCheck,
  selectedCount,
  onClear,
  onSell,
  sellLoading = false,
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
  }, [onCheck, onClear, onSell, selectedCount, sellLoading, t]);
}
