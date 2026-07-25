-- +goose Up

UPDATE system_settings
SET `value` = CAST(CEIL(CAST(TRIM(`value`) AS DECIMAL(18,6)) * 100) / 100 AS CHAR)
WHERE `key` = 'topup_fee_cap'
  AND TRIM(`value`) REGEXP '^[0-9]+(\\.[0-9]{1,6})?$'
  AND CAST(TRIM(`value`) AS DECIMAL(18,6)) <> ROUND(CAST(TRIM(`value`) AS DECIMAL(18,6)), 2);

-- +goose Down

-- Fractional-cent caps cannot be reconstructed after normalization.
SELECT 1;
