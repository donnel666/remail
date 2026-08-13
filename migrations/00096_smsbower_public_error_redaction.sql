-- +goose Up

UPDATE smsbower_orders
SET last_safe_error = CASE
    WHEN status IN ('completed', 'cancelled') THEN ''
    WHEN remote_mail_id IS NULL THEN '下单失败'
    ELSE '接码失败'
END
WHERE last_safe_error <> '';

UPDATE order_events AS oe
JOIN smsbower_orders AS so ON so.order_no = oe.order_no
SET oe.reason = CASE
    WHEN oe.event_type = 'order.failed' THEN '下单失败'
    WHEN oe.event_type = 'order.refunded' THEN '接码失败'
    ELSE REPLACE(oe.reason, 'SMSBower 接码生命周期已结束', '接码服务已结束')
END
WHERE oe.operator_type = 'system'
  AND (
      oe.event_type IN ('order.failed', 'order.refunded')
      OR (
          oe.event_type = 'order.completed'
          AND oe.reason LIKE 'SMSBower 接码生命周期已结束，共接收 % 个验证码。'
      )
  );

-- +goose Down

-- Public error redaction is intentionally irreversible.
SELECT 1;
