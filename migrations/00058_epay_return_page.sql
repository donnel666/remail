-- +goose Up

UPDATE system_settings
SET value = CONCAT(SUBSTRING_INDEX(value, '/wallet', 1), '/payment/return')
WHERE `key` = 'epay_return_url'
  AND value REGEXP '^https://[^/?#]+/wallet/?$';

-- +goose Down

UPDATE system_settings
SET value = CONCAT(SUBSTRING_INDEX(value, '/payment/return', 1), '/wallet')
WHERE `key` = 'epay_return_url'
  AND value REGEXP '^https://[^/?#]+/payment/return/?$';
