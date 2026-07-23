-- +goose Up

-- Mail content is retained in mailmatch_messages while matching ownership and
-- derived codes move to an independently readable projection. The numeric
-- message ID remains the public and foreign-key identity.
CREATE TABLE mailmatch_message_projections (
    message_id BIGINT UNSIGNED NOT NULL,
    matched_order_id BIGINT UNSIGNED NULL,
    status VARCHAR(32) NOT NULL COMMENT 'received|matched|ignored',
    verification_code VARCHAR(64) NOT NULL DEFAULT '',
    match_diagnostic VARCHAR(500) NOT NULL DEFAULT '',
    message_received_at DATETIME NOT NULL,
    decided_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (message_id),
    INDEX idx_mm_projection_order_time (
        matched_order_id,
        message_received_at,
        message_id
    ),
    CONSTRAINT fk_mm_projection_message
        FOREIGN KEY (message_id) REFERENCES mailmatch_messages(id) ON DELETE CASCADE,
    CONSTRAINT fk_mm_projection_order
        FOREIGN KEY (matched_order_id) REFERENCES orders(id) ON DELETE RESTRICT,
    CONSTRAINT chk_mm_projection_status
        CHECK (status IN ('received', 'matched', 'ignored')),
    CONSTRAINT chk_mm_projection_owner
        CHECK (
            (status = 'matched' AND matched_order_id IS NOT NULL)
            OR (status IN ('received', 'ignored') AND matched_order_id IS NULL)
        )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

-- Preserve projection-only decisions before an application rollback reads the
-- legacy columns again. Stop mailmatch writers before running this downgrade.
UPDATE mailmatch_messages AS m
JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id
SET m.matched_order_id = mp.matched_order_id,
    m.status = mp.status,
    m.verification_code = mp.verification_code,
    m.match_diagnostic = mp.match_diagnostic,
    m.updated_at = GREATEST(m.updated_at, mp.decided_at);

DROP TABLE IF EXISTS mailmatch_message_projections;
