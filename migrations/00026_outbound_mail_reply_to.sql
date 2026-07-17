-- +goose Up

-- Reply-To lets a caller (e.g. the after-sales ticket mailer) route customer
-- replies to a per-ticket plus-address while keeping a clean From. It survives
-- the async send round-trip because the worker rebuilds the message from this
-- row.
ALTER TABLE outbound_mails
    ADD COLUMN reply_to VARCHAR(320) NOT NULL DEFAULT '' AFTER recipient;

-- +goose Down

ALTER TABLE outbound_mails
    DROP COLUMN reply_to;
