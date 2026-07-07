-- +goose Up

ALTER TABLE invites
    ADD COLUMN invite_kind VARCHAR(32) NOT NULL DEFAULT 'admin' AFTER code,
    ADD COLUMN referral_owner_user_id BIGINT UNSIGNED NULL AFTER created_by_user_id,
    ADD CONSTRAINT chk_invites_kind CHECK (invite_kind IN ('admin', 'referral'));

CREATE INDEX idx_invites_kind_created ON invites(invite_kind, created_at);
CREATE UNIQUE INDEX idx_invites_referral_owner ON invites(referral_owner_user_id);
CREATE UNIQUE INDEX idx_invites_code_referral_owner ON invites(code, referral_owner_user_id);

ALTER TABLE invites
    ADD CONSTRAINT fk_invites_referral_owner FOREIGN KEY (referral_owner_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_invites_referral_owner CHECK (
        (invite_kind = 'admin' AND referral_owner_user_id IS NULL)
        OR
        (invite_kind = 'referral' AND referral_owner_user_id IS NOT NULL)
    );

CREATE TABLE referral_rewards (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    inviter_user_id BIGINT UNSIGNED NOT NULL,
    invitee_user_id BIGINT UNSIGNED NOT NULL,
    invite_code VARCHAR(64) NOT NULL,
    source_transaction_id BIGINT UNSIGNED NOT NULL,
    transfer_transaction_id BIGINT UNSIGNED NULL,
    source_amount DECIMAL(18,2) NOT NULL,
    reward_amount DECIMAL(18,2) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'available',
    transferred_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_referral_rewards_invitee (invitee_user_id),
    UNIQUE INDEX idx_referral_rewards_source_transaction (source_transaction_id),
    INDEX idx_referral_rewards_transfer_transaction (transfer_transaction_id),
    INDEX idx_referral_rewards_inviter_created (inviter_user_id, created_at, id),
    INDEX idx_referral_rewards_inviter_status (inviter_user_id, status, id),
    INDEX idx_referral_rewards_invite_code (invite_code),
    CONSTRAINT fk_referral_rewards_inviter FOREIGN KEY (inviter_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_invitee FOREIGN KEY (invitee_user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_invite FOREIGN KEY (invite_code) REFERENCES invites(code) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_invite_owner FOREIGN KEY (invite_code, inviter_user_id) REFERENCES invites(code, referral_owner_user_id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_invite_use FOREIGN KEY (invite_code, invitee_user_id) REFERENCES invite_uses(invite_code, user_id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_source_transaction FOREIGN KEY (source_transaction_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT fk_referral_rewards_transfer_transaction FOREIGN KEY (transfer_transaction_id) REFERENCES wallet_transactions(id) ON DELETE RESTRICT,
    CONSTRAINT chk_referral_rewards_users CHECK (inviter_user_id <> invitee_user_id),
    CONSTRAINT chk_referral_rewards_amount CHECK (source_amount > 0 AND reward_amount > 0),
    CONSTRAINT chk_referral_rewards_status CHECK (status IN ('available', 'transferred')),
    CONSTRAINT chk_referral_rewards_transfer_state CHECK (
        (status = 'available' AND transfer_transaction_id IS NULL AND transferred_at IS NULL)
        OR
        (status = 'transferred' AND transfer_transaction_id IS NOT NULL AND transferred_at IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE IF EXISTS referral_rewards;

ALTER TABLE invites
    DROP FOREIGN KEY fk_invites_referral_owner,
    DROP CHECK chk_invites_referral_owner,
    DROP CHECK chk_invites_kind;

DROP INDEX idx_invites_referral_owner ON invites;
DROP INDEX idx_invites_code_referral_owner ON invites;
DROP INDEX idx_invites_kind_created ON invites;

ALTER TABLE invites
    DROP COLUMN referral_owner_user_id,
    DROP COLUMN invite_kind;
