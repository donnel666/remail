-- +goose Up

ALTER TABLE project_products
    DROP CHECK chk_project_products_gmail;

-- The old Gmail project path forced every enabled code window to 24 hours.
-- Restore that mistaken default without overwriting any other explicit value.
UPDATE project_products
SET code_window_minutes = 10
WHERE type = 'gmail'
  AND code_enabled = 1
  AND code_window_minutes = 1440;

-- +goose Down

-- Restoring the old constraint would overwrite project settings, so refuse a
-- lossy rollback while any enabled Gmail code product exists.
DROP TEMPORARY TABLE IF EXISTS gmail_project_code_window_down_guard;
CREATE TEMPORARY TABLE gmail_project_code_window_down_guard (
    enabled_rows BIGINT NOT NULL,
    CONSTRAINT chk_gmail_project_code_window_down_guard CHECK (enabled_rows = 0)
);
INSERT INTO gmail_project_code_window_down_guard (enabled_rows)
SELECT COUNT(*)
FROM project_products
WHERE type = 'gmail'
  AND code_enabled = 1;
DROP TEMPORARY TABLE gmail_project_code_window_down_guard;

ALTER TABLE project_products
    ADD CONSTRAINT chk_project_products_gmail CHECK (
        type <> 'gmail'
        OR code_enabled = 0
        OR code_window_minutes = 1440
    );
