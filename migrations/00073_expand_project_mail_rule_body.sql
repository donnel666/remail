-- +goose Up

ALTER TABLE project_mail_rules
    MODIFY COLUMN pattern VARCHAR(10000) NOT NULL;

-- +goose Down

DROP TEMPORARY TABLE IF EXISTS project_mail_rule_body_down_guard;
CREATE TEMPORARY TABLE project_mail_rule_body_down_guard (
    long_patterns BIGINT NOT NULL,
    CONSTRAINT chk_project_mail_rule_body_down_guard CHECK (long_patterns = 0)
);
INSERT INTO project_mail_rule_body_down_guard (long_patterns)
SELECT COUNT(*)
FROM project_mail_rules
WHERE CHAR_LENGTH(pattern) > 500;
DROP TEMPORARY TABLE project_mail_rule_body_down_guard;

ALTER TABLE project_mail_rules
    MODIFY COLUMN pattern VARCHAR(500) NOT NULL;
