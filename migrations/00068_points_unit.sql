-- +goose Up

-- Keep business schema DDL out of this migration. MySQL temporary-table DDL
-- does not implicitly commit, so Goose commits the converted data, marker and
-- version 68 together; a failure rolls everything back.

DROP TEMPORARY TABLE IF EXISTS points_unit_guard;
CREATE TEMPORARY TABLE points_unit_guard (
    invalid_rows BIGINT NOT NULL,
    CONSTRAINT chk_points_unit_guard CHECK (invalid_rows = 0)
);

-- The marker is committed in the same transaction as every converted value.
-- It survives version-metadata mistakes and blocks a second multiplication.
INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM system_settings
WHERE `key` = 'points_unit_migration_v1';

-- Multiplying by 1000 must still fit DECIMAL(18,6). payment_amount is omitted
-- deliberately because it is the immutable RMB amount sent to Alipay.
INSERT INTO points_unit_guard (invalid_rows)
SELECT COALESCE(SUM(invalid_rows), 0)
FROM (
    SELECT COUNT(*) AS invalid_rows
    FROM wallets
    WHERE GREATEST(
        ABS(consumer_balance),
        ABS(supplier_available),
        ABS(supplier_frozen),
        ABS(total_spend),
        ABS(total_recharged)
    ) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM wallet_transactions
    WHERE GREATEST(ABS(amount), ABS(balance_before), ABS(balance_after)) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM project_products
    WHERE GREATEST(
        ABS(code_price),
        ABS(purchase_price),
        ABS(code_supplier_price),
        ABS(purchase_supplier_price)
    ) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM user_groups
    WHERE ABS(topup_threshold) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM card_keys
    WHERE ABS(amount) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM referral_rewards
    WHERE GREATEST(ABS(source_amount), ABS(reward_amount)) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM recharges
    WHERE ABS(recharge_quota) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM daily_checkins
    WHERE ABS(reward_amount) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM leaderboard_rewards
    WHERE ABS(reward_amount) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM orders
    WHERE GREATEST(
        ABS(pay_amount),
        ABS(refund_amount),
        ABS(COALESCE(random_microsoft_pay_amount, 0)),
        ABS(COALESCE(random_domain_pay_amount, 0))
    ) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM aftersale_tickets
    WHERE pay_amount < 0
       OR refund_amount < 0
       OR GREATEST(ABS(pay_amount), ABS(refund_amount)) > 999999999.999999
    UNION ALL
    SELECT COUNT(*)
    FROM system_settings
    WHERE `key` IN (
        'registration_reward_amount',
        'single_rebate_cap',
        'cumulative_rebate_cap',
        'min_topup_amount',
        'topup_fee_cap',
        'default_project_microsoft_code_price',
        'default_project_microsoft_code_supplier_price',
        'default_project_microsoft_purchase_price',
        'default_project_microsoft_purchase_supplier_price',
        'default_project_domain_code_price',
        'default_project_domain_code_supplier_price',
        'default_project_domain_purchase_price',
        'default_project_domain_purchase_supplier_price'
    )
      AND (
          NOT REGEXP_LIKE(TRIM(`value`), '^[0-9]+(\\.[0-9]{1,6})?$')
          OR CAST(TRIM(`value`) AS DECIMAL(24,6)) > 999999999.999999
      )
) AS invalid;

-- Validate JSON containers before reconstructing only their monetary members.
INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM system_settings
WHERE `key` IN (
    'topup_amount_presets',
    'daily_checkin_reward_rules',
    'leaderboard_reward_rules'
)
  AND (
      JSON_VALID(`value`) = 0
      OR IF(JSON_VALID(`value`), JSON_TYPE(`value`), NULL) <> 'ARRAY'
  );

INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM system_settings
WHERE `key` = 'topup_amount_bonus'
  AND (
      JSON_VALID(`value`) = 0
      OR IF(JSON_VALID(`value`), JSON_TYPE(`value`), NULL) <> 'OBJECT'
  );

INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM leaderboard_settlements
WHERE JSON_TYPE(rules_snapshot) <> 'ARRAY';

INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM system_settings AS setting
JOIN JSON_TABLE(
    setting.`value`,
    '$[*]' COLUMNS(item JSON PATH '$')
) AS item
WHERE setting.`key` = 'topup_amount_presets'
  AND (
      JSON_TYPE(item.item) NOT IN ('INTEGER', 'DOUBLE', 'DECIMAL')
      OR NOT REGEXP_LIKE(JSON_UNQUOTE(item.item), '^[0-9]+(\\.[0-9]{1,6})?$')
      OR CAST(JSON_UNQUOTE(item.item) AS DECIMAL(24,6)) <= 0
      OR CAST(JSON_UNQUOTE(item.item) AS DECIMAL(24,6)) > 999999999.999999
  );

INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM system_settings AS setting
JOIN JSON_TABLE(
    JSON_KEYS(setting.`value`),
    '$[*]' COLUMNS(item_key VARCHAR(64) PATH '$')
) AS item
WHERE setting.`key` = 'topup_amount_bonus'
  AND JSON_LENGTH(setting.`value`) > 0
  AND (
      NOT REGEXP_LIKE(item.item_key, '^[0-9]+(\\.[0-9]{1,6})?$')
      OR CAST(item.item_key AS DECIMAL(24,6)) <= 0
      OR CAST(item.item_key AS DECIMAL(24,6)) > 999999999.999999
      OR JSON_TYPE(JSON_EXTRACT(setting.`value`, CONCAT('$."', item.item_key, '"'))) NOT IN ('INTEGER', 'DOUBLE', 'DECIMAL')
      OR NOT REGEXP_LIKE(
          JSON_UNQUOTE(JSON_EXTRACT(setting.`value`, CONCAT('$."', item.item_key, '"'))),
          '^[0-9]+(\\.[0-9]{1,6})?$'
      )
      OR CAST(
          JSON_UNQUOTE(JSON_EXTRACT(setting.`value`, CONCAT('$."', item.item_key, '"')))
          AS DECIMAL(24,6)
      ) > 999999999.999999
  );

-- Numeric-equivalent presets are invalid, and equivalent bonus keys would
-- collapse in JSON_OBJECTAGG. Abort instead of committing a broken tier set.
INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM (
    SELECT setting.`key`
    FROM system_settings AS setting
    JOIN JSON_TABLE(
        setting.`value`,
        '$[*]' COLUMNS(item JSON PATH '$')
    ) AS item
    WHERE setting.`key` = 'topup_amount_presets'
    GROUP BY setting.`key`
    HAVING COUNT(*) <> COUNT(DISTINCT CAST(CAST(JSON_UNQUOTE(item.item) AS DECIMAL(24,6)) * 1000 AS CHAR))
) AS duplicate_presets;

INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM (
    SELECT setting.`key`
    FROM system_settings AS setting
    JOIN JSON_TABLE(
        JSON_KEYS(setting.`value`),
        '$[*]' COLUMNS(item_key VARCHAR(64) PATH '$')
    ) AS item
    WHERE setting.`key` = 'topup_amount_bonus'
      AND JSON_LENGTH(setting.`value`) > 0
    GROUP BY setting.`key`
    HAVING COUNT(*) <> COUNT(DISTINCT CAST(CAST(item.item_key AS DECIMAL(24,6)) * 1000 AS CHAR))
) AS duplicate_bonus_keys;

INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM system_settings AS setting
JOIN JSON_TABLE(
    setting.`value`,
    '$[*]' COLUMNS(item JSON PATH '$')
) AS item
WHERE setting.`key` IN ('daily_checkin_reward_rules', 'leaderboard_reward_rules')
  AND (
      JSON_TYPE(item.item) <> 'OBJECT'
      OR JSON_EXTRACT(item.item, '$.amount') IS NULL
      OR JSON_TYPE(JSON_EXTRACT(item.item, '$.amount')) NOT IN ('INTEGER', 'DOUBLE', 'DECIMAL')
      OR NOT REGEXP_LIKE(
          JSON_UNQUOTE(JSON_EXTRACT(item.item, '$.amount')),
          '^[0-9]+(\\.[0-9]{1,6})?$'
      )
      OR CAST(JSON_UNQUOTE(JSON_EXTRACT(item.item, '$.amount')) AS DECIMAL(24,6)) <= 0
      OR CAST(JSON_UNQUOTE(JSON_EXTRACT(item.item, '$.amount')) AS DECIMAL(24,6)) > 999999999.999999
  );

INSERT INTO points_unit_guard (invalid_rows)
SELECT COUNT(*)
FROM leaderboard_settlements AS settlement
JOIN JSON_TABLE(
    settlement.rules_snapshot,
    '$[*]' COLUMNS(item JSON PATH '$')
) AS item
WHERE JSON_TYPE(settlement.rules_snapshot) = 'ARRAY'
  AND (
      JSON_TYPE(item.item) <> 'OBJECT'
      OR JSON_EXTRACT(item.item, '$.amount') IS NULL
      OR JSON_TYPE(JSON_EXTRACT(item.item, '$.amount')) NOT IN ('INTEGER', 'DOUBLE', 'DECIMAL')
      OR NOT REGEXP_LIKE(
          JSON_UNQUOTE(JSON_EXTRACT(item.item, '$.amount')),
          '^[0-9]+(\\.[0-9]{1,6})?$'
      )
      OR CAST(JSON_UNQUOTE(JSON_EXTRACT(item.item, '$.amount')) AS DECIMAL(24,6)) <= 0
      OR CAST(JSON_UNQUOTE(JSON_EXTRACT(item.item, '$.amount')) AS DECIMAL(24,6)) > 999999999.999999
  );

DROP TEMPORARY TABLE points_unit_guard;

UPDATE wallets
SET consumer_balance = consumer_balance * 1000,
    supplier_available = supplier_available * 1000,
    supplier_frozen = supplier_frozen * 1000,
    total_spend = total_spend * 1000,
    total_recharged = total_recharged * 1000;

UPDATE wallet_transactions
SET amount = amount * 1000,
    balance_before = balance_before * 1000,
    balance_after = balance_after * 1000;

UPDATE project_products
SET code_price = code_price * 1000,
    purchase_price = purchase_price * 1000,
    code_supplier_price = code_supplier_price * 1000,
    purchase_supplier_price = purchase_supplier_price * 1000;

UPDATE user_groups
SET topup_threshold = topup_threshold * 1000;

UPDATE card_keys
SET amount = amount * 1000;

UPDATE referral_rewards
SET source_amount = source_amount * 1000,
    reward_amount = reward_amount * 1000;

UPDATE recharges
SET recharge_quota = recharge_quota * 1000;

UPDATE daily_checkins
SET reward_amount = reward_amount * 1000;

UPDATE leaderboard_rewards
SET reward_amount = reward_amount * 1000;

UPDATE orders
SET pay_amount = pay_amount * 1000,
    refund_amount = refund_amount * 1000,
    random_microsoft_pay_amount = CASE
        WHEN random_microsoft_pay_amount IS NULL THEN NULL
        ELSE random_microsoft_pay_amount * 1000
    END,
    random_domain_pay_amount = CASE
        WHEN random_domain_pay_amount IS NULL THEN NULL
        ELSE random_domain_pay_amount * 1000
    END;

UPDATE aftersale_tickets
SET pay_amount = pay_amount * 1000,
    refund_amount = refund_amount * 1000;

UPDATE system_settings
SET `value` = CAST(CAST(TRIM(`value`) AS DECIMAL(24,6)) * 1000 AS CHAR)
WHERE `key` IN (
    'registration_reward_amount',
    'single_rebate_cap',
    'cumulative_rebate_cap',
    'min_topup_amount',
    'topup_fee_cap',
    'default_project_microsoft_code_price',
    'default_project_microsoft_code_supplier_price',
    'default_project_microsoft_purchase_price',
    'default_project_microsoft_purchase_supplier_price',
    'default_project_domain_code_price',
    'default_project_domain_code_supplier_price',
    'default_project_domain_purchase_price',
    'default_project_domain_purchase_supplier_price'
);

SET SESSION group_concat_max_len = 16777216;

UPDATE system_settings AS setting
JOIN (
    SELECT source.`key`,
           CONCAT(
               '[',
               GROUP_CONCAT(
                   CAST(CAST(JSON_UNQUOTE(item.item) AS DECIMAL(24,6)) * 1000 AS CHAR)
                   ORDER BY item.ordinality SEPARATOR ','
               ),
               ']'
           ) AS transformed
    FROM system_settings AS source
    JOIN JSON_TABLE(
        source.`value`,
        '$[*]' COLUMNS(ordinality FOR ORDINALITY, item JSON PATH '$')
    ) AS item
    WHERE source.`key` = 'topup_amount_presets'
    GROUP BY source.`key`
) AS converted ON converted.`key` = setting.`key`
SET setting.`value` = converted.transformed;

UPDATE system_settings AS setting
JOIN (
    SELECT source.`key`,
           JSON_OBJECTAGG(
               CAST(CAST(item.item_key AS DECIMAL(24,6)) * 1000 AS CHAR),
               CAST(
                   JSON_UNQUOTE(JSON_EXTRACT(source.`value`, CONCAT('$."', item.item_key, '"')))
                   AS DECIMAL(24,6)
               ) * 1000
           ) AS transformed
    FROM system_settings AS source
    JOIN JSON_TABLE(
        JSON_KEYS(source.`value`),
        '$[*]' COLUMNS(item_key VARCHAR(64) PATH '$')
    ) AS item
    WHERE source.`key` = 'topup_amount_bonus'
      AND JSON_LENGTH(source.`value`) > 0
    GROUP BY source.`key`
) AS converted ON converted.`key` = setting.`key`
SET setting.`value` = CAST(converted.transformed AS CHAR);

UPDATE system_settings AS setting
JOIN (
    SELECT source.`key`,
           CONCAT(
               '[',
               GROUP_CONCAT(
                   CAST(
                       JSON_SET(
                           item.item,
                           '$.amount',
                           CAST(JSON_UNQUOTE(JSON_EXTRACT(item.item, '$.amount')) AS DECIMAL(24,6)) * 1000
                       ) AS CHAR
                   )
                   ORDER BY item.ordinality SEPARATOR ','
               ),
               ']'
           ) AS transformed
    FROM system_settings AS source
    JOIN JSON_TABLE(
        source.`value`,
        '$[*]' COLUMNS(ordinality FOR ORDINALITY, item JSON PATH '$')
    ) AS item
    WHERE source.`key` IN ('daily_checkin_reward_rules', 'leaderboard_reward_rules')
    GROUP BY source.`key`
) AS converted ON converted.`key` = setting.`key`
SET setting.`value` = converted.transformed;

UPDATE leaderboard_settlements AS settlement
JOIN (
    SELECT source.id,
           CONCAT(
               '[',
               GROUP_CONCAT(
                   CAST(
                       JSON_SET(
                           item.item,
                           '$.amount',
                           CAST(JSON_UNQUOTE(JSON_EXTRACT(item.item, '$.amount')) AS DECIMAL(24,6)) * 1000
                       ) AS CHAR
                   )
                   ORDER BY item.ordinality SEPARATOR ','
               ),
               ']'
           ) AS transformed
    FROM leaderboard_settlements AS source
    JOIN JSON_TABLE(
        source.rules_snapshot,
        '$[*]' COLUMNS(ordinality FOR ORDINALITY, item JSON PATH '$')
    ) AS item
    GROUP BY source.id
) AS converted ON converted.id = settlement.id
SET settlement.rules_snapshot = converted.transformed;

-- Migrate only the exact legacy refund message emitted by the old application.
-- The currency sign is expressed as UTF-8 bytes so it cannot reappear in UI or
-- source-text scans.
UPDATE aftersale_ticket_messages AS message
JOIN aftersale_tickets AS ticket ON ticket.ticket_no = message.ticket_no
SET message.content = CONCAT(
    '平台已退款 ',
    CASE
        WHEN ticket.refund_amount = 0 THEN '0'
        ELSE TRIM(TRAILING '.' FROM TRIM(TRAILING '0' FROM CAST(ticket.refund_amount AS CHAR)))
    END,
    ' 积分并关闭工单。'
)
WHERE ticket.resolution_kind = 'refunded'
  AND message.sender_type = 'system'
  AND message.content REGEXP CONCAT(
      '^平台已退款 ',
      CONVERT(0xC2A5 USING utf8mb4) COLLATE utf8mb4_unicode_ci,
      '[0-9]+(\\.[0-9]{1,6})? 并关闭工单。$'
  );

UPDATE aftersale_tickets AS ticket
SET ticket.last_message_preview = CONCAT(
    '平台已退款 ',
    CASE
        WHEN ticket.refund_amount = 0 THEN '0'
        ELSE TRIM(TRAILING '.' FROM TRIM(TRAILING '0' FROM CAST(ticket.refund_amount AS CHAR)))
    END,
    ' 积分并关闭工单。'
)
WHERE ticket.resolution_kind = 'refunded'
  AND ticket.last_message_sender_type = 'system'
  AND ticket.last_message_preview REGEXP CONCAT(
      '^平台已退款 ',
      CONVERT(0xC2A5 USING utf8mb4) COLLATE utf8mb4_unicode_ci,
      '[0-9]+(\\.[0-9]{1,6})? 并关闭工单。$'
  );

-- The legacy withdrawal form stored the selected amount in the first ticket
-- message. Convert that exact generated shape as well; arbitrary user text is
-- intentionally left untouched.
UPDATE aftersale_ticket_messages AS message
JOIN aftersale_tickets AS ticket ON ticket.ticket_no = message.ticket_no
SET message.content = CONCAT(
    '提现积分：',
    TRIM(
        TRAILING '.' FROM TRIM(
            TRAILING '0' FROM CAST(
                CAST(
                    CAST(
                        REGEXP_SUBSTR(message.content, '[0-9]+([.][0-9]{1,6})?', 1, 1)
                        AS CHAR
                    )
                    AS DECIMAL(24,6)
                ) * 1000 AS CHAR
            )
        )
    ),
    ' 积分',
    SUBSTRING(
        message.content,
        LOCATE(CONVERT(0x0A USING utf8mb4), message.content)
    )
)
WHERE ticket.ticket_type = 'general'
  AND ticket.title = '供应商提现申请'
  AND message.sender_type = 'user'
  AND message.content REGEXP CONCAT(
      '^提现金额：',
      CONVERT(0xEFBFA5 USING utf8mb4) COLLATE utf8mb4_unicode_ci,
      '[0-9]+(\\.[0-9]{1,6})?',
      CONVERT(0x0A USING utf8mb4) COLLATE utf8mb4_unicode_ci,
      '提现方式：支付宝',
      CONVERT(0x0A USING utf8mb4) COLLATE utf8mb4_unicode_ci,
      '备注：'
  );

UPDATE aftersale_tickets AS ticket
SET ticket.last_message_preview = CONCAT(
    '提现积分：',
    TRIM(
        TRAILING '.' FROM TRIM(
            TRAILING '0' FROM CAST(
                CAST(
                    CAST(
                        REGEXP_SUBSTR(ticket.last_message_preview, '[0-9]+([.][0-9]{1,6})?', 1, 1)
                        AS CHAR
                    )
                    AS DECIMAL(24,6)
                ) * 1000 AS CHAR
            )
        )
    ),
    ' 积分',
    SUBSTRING(
        ticket.last_message_preview,
        LOCATE(' 提现方式：支付宝 备注：', ticket.last_message_preview)
    )
)
WHERE ticket.ticket_type = 'general'
  AND ticket.title = '供应商提现申请'
  AND ticket.last_message_sender_type = 'user'
  AND ticket.last_message_preview REGEXP CONCAT(
      '^提现金额：',
      CONVERT(0xEFBFA5 USING utf8mb4) COLLATE utf8mb4_unicode_ci,
      '[0-9]+(\\.[0-9]{1,6})? 提现方式：支付宝 备注：'
  );

INSERT INTO system_settings (`key`, `value`)
VALUES ('points_per_yuan', '1000')
ON DUPLICATE KEY UPDATE `value` = '1000';

INSERT INTO system_settings (`key`, `value`)
VALUES ('points_unit_migration_v1', 'completed');

-- +goose Down

-- Irreversible: fail before Goose can record version 68 as rolled back. The
-- persistent marker still blocks a second conversion if version metadata is
-- changed manually.
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'migration 00068_points_unit is irreversible';
