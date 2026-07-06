-- +goose Up

CREATE TABLE wallets (
    user_id BIGINT UNSIGNED PRIMARY KEY,
    consumer_balance DECIMAL(18,2) NOT NULL DEFAULT 0.00,
    supplier_available DECIMAL(18,2) NOT NULL DEFAULT 0.00,
    supplier_frozen DECIMAL(18,2) NOT NULL DEFAULT 0.00,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT fk_wallets_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_wallets_consumer_nonnegative CHECK (consumer_balance >= 0),
    CONSTRAINT chk_wallets_supplier_available_nonnegative CHECK (supplier_available >= 0),
    CONSTRAINT chk_wallets_supplier_frozen_nonnegative CHECK (supplier_frozen >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE wallet_transactions (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    transaction_no VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    transaction_type VARCHAR(32) NOT NULL,
    balance_bucket VARCHAR(32) NOT NULL,
    direction VARCHAR(8) NOT NULL,
    amount DECIMAL(18,2) NOT NULL,
    balance_before DECIMAL(18,2) NOT NULL,
    balance_after DECIMAL(18,2) NOT NULL,
    biz_type VARCHAR(32) NOT NULL,
    biz_id VARCHAR(128) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_wallet_transactions_no (transaction_no),
    INDEX idx_wallet_transactions_user_created (user_id, created_at, id),
    INDEX idx_wallet_transactions_biz (biz_type, biz_id),
    INDEX idx_wallet_transactions_idempotency (idempotency_key),
    CONSTRAINT fk_wallet_transactions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_wallet_transactions_type CHECK (transaction_type IN ('recharge', 'debit', 'refund', 'freeze', 'credit', 'withdrawal', 'manual_adjustment', 'card_redeem', 'transfer')),
    CONSTRAINT chk_wallet_transactions_bucket CHECK (balance_bucket IN ('consumer', 'supplier_available', 'supplier_frozen')),
    CONSTRAINT chk_wallet_transactions_direction CHECK (direction IN ('in', 'out')),
    CONSTRAINT chk_wallet_transactions_amount CHECK (amount > 0),
    CONSTRAINT chk_wallet_transactions_balance CHECK (balance_before >= 0 AND balance_after >= 0),
    CONSTRAINT chk_wallet_transactions_biz CHECK (biz_type <> '' AND biz_id <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE idempotency_keys (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    owner_user_id BIGINT UNSIGNED NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    operation VARCHAR(64) NOT NULL,
    request_fingerprint CHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'succeeded',
    response_json JSON NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_idempotency_owner_key_operation (owner_user_id, idempotency_key, operation),
    INDEX idx_idempotency_created (created_at),
    CONSTRAINT fk_idempotency_user FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_idempotency_status CHECK (status IN ('processing', 'succeeded', 'failed')),
    CONSTRAINT chk_idempotency_key_nonempty CHECK (idempotency_key <> '' AND operation <> '' AND request_fingerprint <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE recharges (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    recharge_no VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    payment_method VARCHAR(32) NOT NULL,
    recharge_quota DECIMAL(18,2) NOT NULL,
    payment_amount DECIMAL(18,2) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'paying',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_recharges_no (recharge_no),
    INDEX idx_recharges_user_created (user_id, created_at, id),
    INDEX idx_recharges_status_created (status, created_at),
    CONSTRAINT fk_recharges_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_recharges_status CHECK (status IN ('paying', 'callback', 'reconciled', 'credited', 'failed')),
    CONSTRAINT chk_recharges_amount CHECK (recharge_quota > 0 AND payment_amount > 0),
    CONSTRAINT chk_recharges_payment_method CHECK (payment_method <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE card_keys (
    card_key VARCHAR(128) PRIMARY KEY,
    amount DECIMAL(18,2) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'enabled',
    max_redemptions INT NOT NULL DEFAULT 1,
    redeemed_count INT NOT NULL DEFAULT 0,
    expire_at DATETIME NULL,
    created_by_user_id BIGINT UNSIGNED NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_card_keys_status_expire (status, expire_at),
    INDEX idx_card_keys_created_by (created_by_user_id, created_at),
    CONSTRAINT fk_card_keys_created_by FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_card_keys_status CHECK (status IN ('enabled', 'disabled')),
    CONSTRAINT chk_card_keys_amount CHECK (amount > 0),
    CONSTRAINT chk_card_keys_redemptions CHECK (max_redemptions > 0 AND redeemed_count >= 0 AND redeemed_count <= max_redemptions),
    CONSTRAINT chk_card_keys_key_nonempty CHECK (card_key <> '')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE card_key_redemptions (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    card_key VARCHAR(128) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    transaction_id BIGINT UNSIGNED NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    redeemed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_card_redemptions_card_user (card_key, user_id),
    INDEX idx_card_redemptions_user_redeemed (user_id, redeemed_at, id),
    INDEX idx_card_redemptions_transaction (transaction_id),
    CONSTRAINT fk_card_redemptions_card FOREIGN KEY (card_key) REFERENCES card_keys(card_key) ON DELETE RESTRICT,
    CONSTRAINT fk_card_redemptions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_card_redemptions_transaction FOREIGN KEY (transaction_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'billing:wallet', 'read', 'allow'),
    ('p', 'role:admin', 'billing:wallet', 'write', 'allow'),
    ('p', 'role:admin', 'billing:card', 'read', 'allow'),
    ('p', 'role:admin', 'billing:card', 'write', 'allow'),
    ('p', 'role:admin', 'billing:card', 'operate', 'allow'),
    ('p', 'role:super_admin', 'billing:wallet', 'read', 'allow'),
    ('p', 'role:super_admin', 'billing:wallet', 'write', 'allow'),
    ('p', 'role:super_admin', 'billing:card', 'read', 'allow'),
    ('p', 'role:super_admin', 'billing:card', 'write', 'allow'),
    ('p', 'role:super_admin', 'billing:card', 'operate', 'allow');

-- +goose Down
DELETE FROM casbin_rule
WHERE ptype = 'p'
  AND v1 IN ('billing:wallet', 'billing:card');

DROP TABLE IF EXISTS card_key_redemptions;
DROP TABLE IF EXISTS card_keys;
DROP TABLE IF EXISTS recharges;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS wallet_transactions;
DROP TABLE IF EXISTS wallets;
