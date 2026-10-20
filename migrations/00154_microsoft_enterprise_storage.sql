-- +goose NO TRANSACTION
-- +goose Up

DROP PROCEDURE IF EXISTS separate_microsoft_enterprise_00154;
-- +goose StatementBegin
CREATE PROCEDURE separate_microsoft_enterprise_00154()
BEGIN
    DECLARE previous_lock_wait_timeout BIGINT DEFAULT @@SESSION.lock_wait_timeout;
    DECLARE previous_innodb_lock_wait_timeout BIGINT DEFAULT @@SESSION.innodb_lock_wait_timeout;
    DECLARE previous_isolation VARCHAR(64) DEFAULT @@SESSION.transaction_isolation;
    DECLARE EXIT HANDLER FOR SQLEXCEPTION
    BEGIN
        ROLLBACK;
        SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
        SET SESSION innodb_lock_wait_timeout = previous_innodb_lock_wait_timeout;
        SET SESSION transaction_isolation = previous_isolation;
        RESIGNAL;
    END;
    SET SESSION lock_wait_timeout = 5;
    SET SESSION innodb_lock_wait_timeout = 5;
    SET SESSION transaction_isolation = 'READ-COMMITTED';

    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_email_resources_type' AND check_clause LIKE '%microsoft_enterprise%') THEN
        ALTER TABLE email_resources DROP CHECK chk_email_resources_type,
            ADD CONSTRAINT chk_email_resources_type CHECK (type IN ('microsoft','microsoft_enterprise','domain','gmail','icloud','proto','daum')) NOT ENFORCED, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_allocation_order_guards_type' AND check_clause LIKE '%microsoft_enterprise%') THEN
        ALTER TABLE allocation_order_guards DROP CHECK chk_allocation_order_guards_type,
            ADD CONSTRAINT chk_allocation_order_guards_type CHECK (type IN ('microsoft','microsoft_enterprise','domain','gmail','icloud','proto','daum')) NOT ENFORCED, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_orders_allocation_shape' AND check_clause LIKE '%microsoft_enterprise%') THEN
        ALTER TABLE orders DROP CHECK chk_orders_allocation_shape,
            ADD CONSTRAINT chk_orders_allocation_shape CHECK (allocation_type IS NULL OR allocation_type IN ('microsoft','microsoft_enterprise','domain','gmail','icloud','proto','daum')) NOT ENFORCED, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_mailmatch_messages_resource_type' AND check_clause LIKE '%microsoft_enterprise%') THEN
        ALTER TABLE mailmatch_messages DROP CHECK chk_mailmatch_messages_resource_type,
            ADD CONSTRAINT chk_mailmatch_messages_resource_type CHECK (resource_type IN ('microsoft','microsoft_enterprise','domain','gmail','icloud','proto','daum')) NOT ENFORCED, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_mailmatch_admin_fetch_operation' AND check_clause LIKE '%enterprise_fetch%') THEN
        ALTER TABLE mailmatch_admin_resource_fetch_states DROP CHECK chk_mailmatch_admin_fetch_operation,
            ADD CONSTRAINT chk_mailmatch_admin_fetch_operation CHECK (operation_kind IN ('resource_fetch','enterprise_fetch','gmail_resource_fetch','icloud_resource_fetch','proto_resource_fetch','daum_resource_fetch')) NOT ENFORCED, ALGORITHM=INSTANT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.check_constraints WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_mailmatch_fetch_state_operation' AND check_clause LIKE '%enterprise_history%') THEN
        ALTER TABLE mailmatch_resource_fetch_states DROP CHECK chk_mailmatch_fetch_state_operation,
            ADD CONSTRAINT chk_mailmatch_fetch_state_operation CHECK (operation_kind IN ('order_fetch','resource_fetch','resource_history','enterprise_history','icloud_resource_fetch')) NOT ENFORCED, ALGORITHM=INSTANT;
    END IF;

    CREATE TABLE IF NOT EXISTS microsoft_enterprise_resources (
        id BIGINT UNSIGNED PRIMARY KEY,
        resource_type VARCHAR(32) NOT NULL DEFAULT 'microsoft_enterprise',
        email_address VARCHAR(255) NOT NULL,
        email_domain VARCHAR(255) NOT NULL,
        password VARCHAR(512) NOT NULL DEFAULT '',
        client_id VARCHAR(255) NOT NULL DEFAULT '',
        refresh_token VARCHAR(8192) NOT NULL DEFAULT '',
        credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
        credential_updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
        long_lived BOOLEAN NOT NULL DEFAULT FALSE,
        graph_available BOOLEAN NOT NULL DEFAULT FALSE,
        rt_expire_at DATETIME NULL,
        token_last_refreshed_at DATETIME(3) NULL,
        token_last_request_id VARCHAR(64) NOT NULL DEFAULT '',
        token_refresh_status VARCHAR(32) NOT NULL DEFAULT 'normal',
        token_refresh_generation BIGINT UNSIGNED NOT NULL DEFAULT 0,
        token_refresh_failures INT NOT NULL DEFAULT 0,
        token_refresh_expected_credential_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
        token_refresh_operator_user_id BIGINT UNSIGNED NULL,
        token_refresh_idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
        token_refresh_request_id VARCHAR(64) NOT NULL DEFAULT '',
        token_refresh_path VARCHAR(255) NOT NULL DEFAULT '',
        token_refresh_last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        token_refresh_requested_at DATETIME(3) NULL,
        token_refresh_started_at DATETIME(3) NULL,
        token_refresh_finished_at DATETIME(3) NULL,
        token_refresh_idempotency_scope VARCHAR(320) GENERATED ALWAYS AS (CASE WHEN token_refresh_operator_user_id IS NOT NULL AND token_refresh_idempotency_key <> '' THEN CONCAT(token_refresh_operator_user_id, ':', token_refresh_idempotency_key) ELSE NULL END) STORED,
        UNIQUE INDEX uq_ms_enterprise_token_idempotency (token_refresh_idempotency_scope),
        INDEX idx_ms_enterprise_token_pending (token_refresh_status, token_refresh_requested_at, id),
        for_sale BOOLEAN NOT NULL DEFAULT FALSE,
        status VARCHAR(32) NOT NULL DEFAULT 'pending',
        validation_generation BIGINT UNSIGNED NOT NULL DEFAULT 1,
        validation_failures INT NOT NULL DEFAULT 0,
        quality_score INT NOT NULL DEFAULT 0,
        plus_daily_limit INT NOT NULL DEFAULT 0,
        alloc_bucket SMALLINT UNSIGNED NOT NULL DEFAULT 0,
        last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        last_allocated_at DATETIME NULL,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE INDEX uq_ms_enterprise_email (email_address),
        INDEX idx_ms_enterprise_status (status, updated_at, id),
        INDEX idx_ms_enterprise_suffix (email_domain, id),
        INDEX idx_microsoft_alloc_public (alloc_bucket, for_sale, status, last_allocated_at, quality_score, id),
        INDEX idx_microsoft_suffix_bucket (email_domain, alloc_bucket),
        CONSTRAINT fk_ms_enterprise_root FOREIGN KEY (id, resource_type) REFERENCES email_resources(id, type) ON DELETE CASCADE,
        CONSTRAINT chk_ms_enterprise_type CHECK (resource_type = 'microsoft_enterprise'),
        CONSTRAINT chk_ms_enterprise_status CHECK (status IN ('pending','validating','identifying','normal','abnormal','disabled','deleted')),
        CONSTRAINT chk_ms_enterprise_quality CHECK (quality_score BETWEEN 0 AND 100),
        CONSTRAINT chk_ms_enterprise_validation_failures CHECK (validation_failures BETWEEN 0 AND 3),
        CONSTRAINT chk_ms_enterprise_token_status CHECK (token_refresh_status IN ('pending','processing','normal','abnormal')),
        CONSTRAINT chk_ms_enterprise_token_failures CHECK (token_refresh_failures BETWEEN 0 AND 3),
        CONSTRAINT chk_ms_enterprise_domain CHECK (email_domain LIKE '%.onmicrosoft.com')
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS microsoft_enterprise_allocations (
        id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
        order_no VARCHAR(64) NOT NULL,
        guard_type VARCHAR(32) NOT NULL DEFAULT 'microsoft_enterprise',
        project_id BIGINT UNSIGNED NOT NULL,
        product_id BIGINT UNSIGNED NOT NULL,
        resource_id BIGINT UNSIGNED NOT NULL,
        supply_scope VARCHAR(16) NOT NULL,
        mailbox VARCHAR(32) NOT NULL DEFAULT 'main',
        explicit_alias_id BIGINT UNSIGNED NULL,
        dot_alias_id BIGINT UNSIGNED NULL,
        plus_alias_id BIGINT UNSIGNED NULL,
        email VARCHAR(255) NOT NULL,
        status VARCHAR(32) NOT NULL DEFAULT 'allocated',
        active_kind TINYINT GENERATED ALWAYS AS (CASE WHEN status='allocated' THEN 1 ELSE NULL END) STORED,
        active_project_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN status='allocated' THEN project_id ELSE NULL END) STORED,
        active_entity_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN status='allocated' THEN resource_id ELSE NULL END) STORED,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        released_at DATETIME NULL,
        UNIQUE INDEX uq_ms_enterprise_allocation_order (order_no),
        UNIQUE INDEX uq_ms_enterprise_allocation_active (active_kind, active_project_id, active_entity_id),
        INDEX idx_ms_enterprise_allocation_resource (resource_id, project_id, status),
        INDEX idx_ms_enterprise_allocation_project (project_id, created_at, id),
        CONSTRAINT fk_ms_enterprise_allocation_guard FOREIGN KEY (order_no, guard_type) REFERENCES allocation_order_guards(order_no, type) ON DELETE RESTRICT,
        CONSTRAINT fk_ms_enterprise_allocation_resource FOREIGN KEY (resource_id) REFERENCES microsoft_enterprise_resources(id) ON DELETE RESTRICT,
        CONSTRAINT fk_ms_enterprise_allocation_product FOREIGN KEY (product_id, project_id) REFERENCES project_products(id, project_id) ON DELETE RESTRICT,
        CONSTRAINT chk_ms_enterprise_allocation_guard CHECK (guard_type='microsoft_enterprise'),
        CONSTRAINT chk_ms_enterprise_allocation_mailbox CHECK (mailbox='main' AND explicit_alias_id IS NULL AND dot_alias_id IS NULL AND plus_alias_id IS NULL),
        CONSTRAINT chk_ms_enterprise_allocation_scope CHECK (supply_scope IN ('owned','public')),
        CONSTRAINT chk_ms_enterprise_allocation_status CHECK (status IN ('allocated','released'))
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS microsoft_enterprise_project_matches (
        resource_id BIGINT UNSIGNED NOT NULL,
        project_id BIGINT UNSIGNED NOT NULL,
        first_matched_at DATETIME(3) NOT NULL,
        last_matched_at DATETIME(3) NOT NULL,
        evidence_count INT UNSIGNED NOT NULL DEFAULT 1,
        last_scanned_at DATETIME(3) NOT NULL,
        created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
        updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        PRIMARY KEY (resource_id, project_id),
        INDEX idx_ms_enterprise_matches_project (project_id, resource_id),
        CONSTRAINT fk_ms_enterprise_matches_resource FOREIGN KEY (resource_id) REFERENCES microsoft_enterprise_resources(id) ON DELETE CASCADE,
        CONSTRAINT fk_ms_enterprise_matches_project FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS microsoft_enterprise_history_scans (
        id BIGINT UNSIGNED PRIMARY KEY,
        history_scan_status VARCHAR(32) NOT NULL DEFAULT 'normal',
        history_scan_generation BIGINT UNSIGNED NOT NULL DEFAULT 0,
        history_scan_failures INT UNSIGNED NOT NULL DEFAULT 0,
        history_scan_scanned_count INT UNSIGNED NOT NULL DEFAULT 0,
        history_scan_matched_count INT UNSIGNED NOT NULL DEFAULT 0,
        history_scan_skipped_count INT UNSIGNED NOT NULL DEFAULT 0,
        history_scan_request_id VARCHAR(64) NOT NULL DEFAULT '',
        history_scan_last_safe_error VARCHAR(500) NOT NULL DEFAULT '',
        history_scan_requested_at DATETIME(3) NULL,
        history_scan_started_at DATETIME(3) NULL,
        history_scan_finished_at DATETIME(3) NULL,
        updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
        INDEX idx_ms_enterprise_history_pending (history_scan_status, history_scan_requested_at, id),
        CONSTRAINT fk_ms_enterprise_history_project FOREIGN KEY (id) REFERENCES projects(id) ON DELETE CASCADE,
        CONSTRAINT chk_ms_enterprise_history_status CHECK (history_scan_status IN ('pending','processing','normal','abnormal')),
        CONSTRAINT chk_ms_enterprise_history_failures CHECK (history_scan_failures <= 3)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    INSERT IGNORE INTO microsoft_enterprise_history_scans(id, history_scan_status, history_scan_generation, history_scan_requested_at)
    SELECT DISTINCT p.id, 'pending', 1, CURRENT_TIMESTAMP(3)
    FROM projects p JOIN project_products pp ON pp.project_id=p.id AND pp.type='microsoft_enterprise'
    WHERE p.status IN ('listed','delisted');

    -- One covering-index pass captures only legacy enterprise IDs. READ COMMITTED
    -- avoids shared range locks over personal resources during this initial scan.
    DROP TEMPORARY TABLE IF EXISTS tmp_ms_enterprise_ids;
    CREATE TEMPORARY TABLE tmp_ms_enterprise_ids (id BIGINT UNSIGNED PRIMARY KEY) ENGINE=InnoDB;
    INSERT INTO tmp_ms_enterprise_ids SELECT id FROM microsoft_resources FORCE INDEX (idx_microsoft_suffix_bucket) WHERE email_domain LIKE '%.onmicrosoft.com';
    DROP TEMPORARY TABLE IF EXISTS tmp_ms_enterprise_rows;
    CREATE TEMPORARY TABLE tmp_ms_enterprise_rows LIKE microsoft_resources;
    DROP TEMPORARY TABLE IF EXISTS tmp_ms_enterprise_matches;
    CREATE TEMPORARY TABLE tmp_ms_enterprise_matches LIKE microsoft_resource_project_matches;

    START TRANSACTION;
    UPDATE resource_imports SET dispatch_status='pending', generation=generation+1,
        claim_token='', started_at=NULL
    WHERE enterprise=TRUE AND status='processing' AND dispatch_status IN ('queued','running');
    UPDATE email_resources er JOIN tmp_ms_enterprise_ids ids ON ids.id=er.id SET er.version=er.version+1 WHERE er.type='microsoft';
    IF EXISTS (SELECT 1 FROM microsoft_allocations dependency JOIN tmp_ms_enterprise_ids ids ON ids.id=dependency.resource_id) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Enterprise resources have legacy allocation or alias dependencies; refusing an unsafe automatic move';
    END IF;
    IF EXISTS (SELECT 1 FROM explicit_aliases dependency JOIN tmp_ms_enterprise_ids ids ON ids.id=dependency.resource_id) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Enterprise resources have legacy allocation or alias dependencies; refusing an unsafe automatic move';
    END IF;
    IF EXISTS (SELECT 1 FROM dot_aliases dependency JOIN tmp_ms_enterprise_ids ids ON ids.id=dependency.resource_id) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Enterprise resources have legacy allocation or alias dependencies; refusing an unsafe automatic move';
    END IF;
    IF EXISTS (SELECT 1 FROM plus_aliases dependency JOIN tmp_ms_enterprise_ids ids ON ids.id=dependency.resource_id) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Enterprise resources have legacy allocation or alias dependencies; refusing an unsafe automatic move';
    END IF;
    IF EXISTS (SELECT 1 FROM microsoft_binding_mailboxes dependency JOIN tmp_ms_enterprise_ids ids ON ids.id=dependency.resource_id) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Enterprise resources have legacy allocation or alias dependencies; refusing an unsafe automatic move';
    END IF;
    IF EXISTS (SELECT 1 FROM mailmatch_messages dependency JOIN tmp_ms_enterprise_ids ids ON ids.id=dependency.email_resource_id) THEN
        SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Enterprise resources have legacy message dependencies; refusing an unsafe automatic move';
    END IF;
    INSERT INTO tmp_ms_enterprise_rows (id,email_address,email_domain,password,client_id,refresh_token,credential_revision,credential_updated_at,graph_available,rt_expire_at,token_last_refreshed_at,token_last_request_id,token_refresh_status,token_refresh_generation,token_refresh_failures,token_refresh_expected_credential_revision,token_refresh_operator_user_id,token_refresh_idempotency_key,token_refresh_request_id,token_refresh_path,token_refresh_last_safe_error,token_refresh_requested_at,token_refresh_started_at,token_refresh_finished_at,for_sale,status,validation_generation,validation_failures,quality_score,alloc_bucket,last_safe_error,last_allocated_at,created_at,updated_at)
    SELECT mr.id,mr.email_address,mr.email_domain,mr.password,mr.client_id,mr.refresh_token,mr.credential_revision,mr.credential_updated_at,mr.graph_available,mr.rt_expire_at,mr.token_last_refreshed_at,mr.token_last_request_id,mr.token_refresh_status,mr.token_refresh_generation,mr.token_refresh_failures,mr.token_refresh_expected_credential_revision,mr.token_refresh_operator_user_id,mr.token_refresh_idempotency_key,mr.token_refresh_request_id,mr.token_refresh_path,mr.token_refresh_last_safe_error,mr.token_refresh_requested_at,mr.token_refresh_started_at,mr.token_refresh_finished_at,mr.for_sale,mr.status,mr.validation_generation,mr.validation_failures,mr.quality_score,mr.alloc_bucket,mr.last_safe_error,mr.last_allocated_at,mr.created_at,mr.updated_at
    FROM microsoft_resources mr JOIN tmp_ms_enterprise_ids ids ON ids.id=mr.id FOR UPDATE;
    INSERT INTO tmp_ms_enterprise_matches SELECT m.* FROM microsoft_resource_project_matches m JOIN tmp_ms_enterprise_ids ids ON ids.id=m.resource_id;
    -- Fence old worker payloads and let the independent dispatchers resume them.
    UPDATE tmp_ms_enterprise_rows SET token_refresh_status='pending',
        token_refresh_generation=token_refresh_generation+1,
        token_refresh_started_at=NULL, token_refresh_finished_at=NULL
    WHERE token_refresh_status IN ('pending','processing');
    UPDATE mailmatch_resource_fetch_states state JOIN tmp_ms_enterprise_ids ids ON ids.id=state.email_resource_id
    SET state.operation_kind='enterprise_history', state.generation=state.generation+1,
        state.started_at=NULL,
        state.status=CASE WHEN state.status='processing' THEN 'pending' ELSE state.status END
    WHERE state.operation_kind='resource_history';
    UPDATE mailmatch_admin_resource_fetch_states state JOIN tmp_ms_enterprise_ids ids ON ids.id=state.email_resource_id
    SET state.operation_kind='enterprise_fetch', state.generation=state.generation+1,
        state.started_at=NULL,
        state.status=CASE WHEN state.status='processing' THEN 'pending' ELSE state.status END
    WHERE state.operation_kind='resource_fetch';
    DELETE mr FROM microsoft_resources mr JOIN tmp_ms_enterprise_ids ids ON ids.id=mr.id;
    UPDATE email_resources er JOIN tmp_ms_enterprise_ids ids ON ids.id=er.id SET er.type='microsoft_enterprise';
    INSERT INTO microsoft_enterprise_resources (id,email_address,email_domain,password,client_id,refresh_token,credential_revision,credential_updated_at,graph_available,rt_expire_at,token_last_refreshed_at,token_last_request_id,token_refresh_status,token_refresh_generation,token_refresh_failures,token_refresh_expected_credential_revision,token_refresh_operator_user_id,token_refresh_idempotency_key,token_refresh_request_id,token_refresh_path,token_refresh_last_safe_error,token_refresh_requested_at,token_refresh_started_at,token_refresh_finished_at,for_sale,status,validation_generation,validation_failures,quality_score,alloc_bucket,last_safe_error,last_allocated_at,created_at,updated_at)
    SELECT id,email_address,email_domain,password,client_id,refresh_token,credential_revision,credential_updated_at,graph_available,rt_expire_at,token_last_refreshed_at,token_last_request_id,token_refresh_status,token_refresh_generation,token_refresh_failures,token_refresh_expected_credential_revision,token_refresh_operator_user_id,token_refresh_idempotency_key,token_refresh_request_id,token_refresh_path,token_refresh_last_safe_error,token_refresh_requested_at,token_refresh_started_at,token_refresh_finished_at,for_sale,
        CASE WHEN status IN ('validating','identifying') THEN 'pending' ELSE status END,
        validation_generation+1,validation_failures,quality_score,alloc_bucket,last_safe_error,last_allocated_at,created_at,updated_at FROM tmp_ms_enterprise_rows;
    INSERT INTO microsoft_enterprise_project_matches SELECT * FROM tmp_ms_enterprise_matches;
    COMMIT;
    DROP TEMPORARY TABLE tmp_ms_enterprise_matches;
    DROP TEMPORARY TABLE tmp_ms_enterprise_rows;
    DROP TEMPORARY TABLE tmp_ms_enterprise_ids;
    SET SESSION transaction_isolation = previous_isolation;
    SET SESSION lock_wait_timeout = previous_lock_wait_timeout;
    SET SESSION innodb_lock_wait_timeout = previous_innodb_lock_wait_timeout;
END;
-- +goose StatementEnd
CALL separate_microsoft_enterprise_00154();
DROP PROCEDURE separate_microsoft_enterprise_00154;

-- +goose Down
-- +goose StatementBegin
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Enterprise storage contains independent business data; automatic rollback is disabled';
-- +goose StatementEnd
