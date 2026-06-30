-- +goose Up
-- +goose StatementBegin

CREATE TABLE users (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    email       VARCHAR(255) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    nickname    VARCHAR(100) NOT NULL DEFAULT '',
    enabled     TINYINT(1) NOT NULL DEFAULT 1,
    role_level  INT NOT NULL DEFAULT 10 COMMENT '10=user 20=supplier 80=admin 100=super_admin',
    token_version INT NOT NULL DEFAULT 0 COMMENT 'increment to invalidate all sessions',
    last_login_at DATETIME NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_users_email (email),
    CONSTRAINT chk_users_role_level CHECK (role_level IN (10, 20, 80, 100))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE system_guard (
    id INT PRIMARY KEY,
    label VARCHAR(64) NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO system_guard (id, label) VALUES (1, 'activation-guard');

CREATE TABLE invites (
    code VARCHAR(64) PRIMARY KEY,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    max_use INT NOT NULL,
    used INT NOT NULL DEFAULT 0,
    expire_at DATETIME NULL,
    created_by_user_id BIGINT UNSIGNED NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_invites_enabled_expire (enabled, expire_at),
    INDEX idx_invites_created_by (created_by_user_id),
    CONSTRAINT fk_invites_created_by FOREIGN KEY (created_by_user_id) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT chk_invites_max_use CHECK (max_use > 0),
    CONSTRAINT chk_invites_used CHECK (used >= 0 AND used <= max_use)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE invite_uses (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    invite_code VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    used_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_invite_uses_invite_user (invite_code, user_id),
    INDEX idx_invite_uses_user (user_id),
    CONSTRAINT fk_invite_uses_invite FOREIGN KEY (invite_code) REFERENCES invites(code) ON DELETE RESTRICT,
    CONSTRAINT fk_invite_uses_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE third_party_identities (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    provider VARCHAR(50) NOT NULL,
    provider_user_id VARCHAR(255) NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_third_party_provider_user (provider, provider_user_id),
    INDEX idx_third_party_user (user_id),
    CONSTRAINT fk_third_party_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE user_login_devices (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    fingerprint VARCHAR(128) NOT NULL,
    last_login_at DATETIME NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_user_login_devices_user_fingerprint (user_id, fingerprint),
    CONSTRAINT fk_user_login_devices_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE casbin_rule (
    id    BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    ptype VARCHAR(100) NOT NULL DEFAULT '',
    v0    VARCHAR(255) NOT NULL DEFAULT '',
    v1    VARCHAR(255) NOT NULL DEFAULT '',
    v2    VARCHAR(255) NOT NULL DEFAULT '',
    v3    VARCHAR(255) NOT NULL DEFAULT '',
    v4    VARCHAR(255) NOT NULL DEFAULT '',
    v5    VARCHAR(255) NOT NULL DEFAULT '',
    INDEX idx_casbin_ptype (ptype)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
    ('p', 'role:admin', 'iam:user', 'read', 'allow'),
    ('p', 'role:admin', 'iam:user', 'write', 'allow'),
    ('p', 'role:admin', 'iam:user', 'operate', 'allow'),
    ('p', 'role:admin', 'iam:permission', 'read', 'allow'),
    ('p', 'role:admin', 'iam:permission', 'write', 'allow'),
    ('p', 'role:admin', 'iam:invite', 'read', 'allow'),
    ('p', 'role:admin', 'iam:invite', 'write', 'allow'),
    ('p', 'role:admin', 'iam:invite', 'operate', 'allow'),
    ('p', 'role:super_admin', 'iam:user', 'read', 'allow'),
    ('p', 'role:super_admin', 'iam:user', 'write', 'allow'),
    ('p', 'role:super_admin', 'iam:user', 'operate', 'allow'),
    ('p', 'role:super_admin', 'iam:permission', 'read', 'allow'),
    ('p', 'role:super_admin', 'iam:permission', 'write', 'allow'),
    ('p', 'role:super_admin', 'iam:invite', 'read', 'allow'),
    ('p', 'role:super_admin', 'iam:invite', 'write', 'allow'),
    ('p', 'role:super_admin', 'iam:invite', 'operate', 'allow');

CREATE TABLE operation_logs (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    operator_user_id BIGINT UNSIGNED NOT NULL,
    operation_type VARCHAR(100) NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    resource_id VARCHAR(100) NOT NULL,
    path VARCHAR(255) NOT NULL,
    result VARCHAR(32) NOT NULL,
    safe_summary VARCHAR(500) NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_operation_logs_operator_created (operator_user_id, created_at),
    INDEX idx_operation_logs_resource_created (resource_type, resource_id, created_at),
    INDEX idx_operation_logs_request_id (request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS operation_logs;
DROP TABLE IF EXISTS casbin_rule;
DROP TABLE IF EXISTS user_login_devices;
DROP TABLE IF EXISTS third_party_identities;
DROP TABLE IF EXISTS invite_uses;
DROP TABLE IF EXISTS invites;
DROP TABLE IF EXISTS system_guard;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
