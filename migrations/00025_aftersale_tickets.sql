-- +goose Up

-- Aftersale support tickets. Simplified model: suppliers fully delegate to the
-- platform, so a ticket is a conversation between the requester (user) and the
-- platform (admin/support). Order display fields are snapshotted at creation
-- because they are immutable for the order's life; ownership and refunds are
-- still resolved live through BC-TRADE.
CREATE TABLE aftersale_tickets (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    ticket_no VARCHAR(64) NOT NULL,
    ticket_type VARCHAR(32) NOT NULL COMMENT 'order|general',
    title VARCHAR(200) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'open' COMMENT 'open|processing|closed',
    requester_user_id BIGINT UNSIGNED NOT NULL,
    -- Secret per-ticket token, embedded in the outbound Reply-To plus-address so
    -- inbound email replies can be correlated without allowing forgery.
    reply_token VARCHAR(64) NOT NULL DEFAULT '',
    -- Order snapshot (empty/null for general tickets).
    order_no VARCHAR(64) NOT NULL DEFAULT '',
    project_name VARCHAR(255) NOT NULL DEFAULT '',
    project_logo_url VARCHAR(1024) NOT NULL DEFAULT '',
    delivery_email VARCHAR(255) NOT NULL DEFAULT '',
    pay_amount DECIMAL(18,6) NOT NULL DEFAULT 0,
    service_mode VARCHAR(32) NOT NULL DEFAULT '' COMMENT 'code|purchase',
    after_sale_until DATETIME(3) NULL,
    -- Resolution (empty until closed).
    resolution_kind VARCHAR(32) NOT NULL DEFAULT '' COMMENT 'refunded|closed',
    refund_amount DECIMAL(18,6) NOT NULL DEFAULT 0,
    -- Per-side unread counters, mirroring the console inbox semantics.
    requester_unread_count INT NOT NULL DEFAULT 0,
    platform_unread_count INT NOT NULL DEFAULT 0,
    -- Denormalized last-message preview so the inbox list needs no message join.
    last_message_preview VARCHAR(500) NOT NULL DEFAULT '',
    last_message_sender_type VARCHAR(32) NOT NULL DEFAULT '',
    last_message_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_aftersale_tickets_ticket_no (ticket_no),
    INDEX idx_aftersale_tickets_requester (requester_user_id, id),
    INDEX idx_aftersale_tickets_status (status),
    INDEX idx_aftersale_tickets_type (ticket_type),
    INDEX idx_aftersale_tickets_order_no (order_no),
    CONSTRAINT chk_aftersale_tickets_type CHECK (ticket_type IN ('order', 'general')),
    CONSTRAINT chk_aftersale_tickets_status CHECK (status IN ('open', 'processing', 'closed'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE aftersale_ticket_messages (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    ticket_no VARCHAR(64) NOT NULL,
    sender_type VARCHAR(32) NOT NULL COMMENT 'user|platform|system',
    sender_user_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    sender_name VARCHAR(128) NOT NULL DEFAULT '',
    sender_email VARCHAR(255) NOT NULL DEFAULT '',
    content VARCHAR(2000) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_aftersale_messages_ticket (ticket_no, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Image attachments live in private object storage (MinIO); only safe metadata
-- is kept here. The object_key is never exposed to clients.
CREATE TABLE aftersale_ticket_attachments (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    attachment_no VARCHAR(64) NOT NULL,
    ticket_no VARCHAR(64) NOT NULL,
    message_id BIGINT UNSIGNED NOT NULL,
    object_key VARCHAR(255) NOT NULL,
    mime VARCHAR(128) NOT NULL DEFAULT '',
    size INT NOT NULL DEFAULT 0,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE INDEX idx_aftersale_attachments_no (attachment_no),
    INDEX idx_aftersale_attachments_message (message_id),
    INDEX idx_aftersale_attachments_ticket (ticket_no)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose Down

DROP TABLE IF EXISTS aftersale_ticket_attachments;
DROP TABLE IF EXISTS aftersale_ticket_messages;
DROP TABLE IF EXISTS aftersale_tickets;
