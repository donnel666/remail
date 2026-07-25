package runtimeconfig

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

type integerRange struct {
	min int64
	max int64
}

func positive(maximum int64) integerRange { return integerRange{min: 1, max: maximum} }

var integerRanges = map[string]integerRange{
	"default_plus_daily_limit": positive(2_147_483_647), "default_mailbox_daily_limit": positive(2_147_483_647), "resource_validation_max_failures": positive(100),
	"resource_import_max_bytes": positive(512 << 20), "max_project_logo_bytes": positive(20 << 20), "project_name_max": positive(120), "project_description_max": positive(1000), "project_target_platform_max": positive(120),
	"candidate_window_size": positive(100), "global_candidate_window": positive(100), "bucket_probe_count": positive(64), "alias_generation_window": positive(1000),
	"candidate_retry_count": positive(20), "dot_alias_capacity_per_resource": positive(64), "inventory_refresh_interval_minutes": positive(1440), "inventory_cache_activity_ttl_minutes": positive(43200), "inventory_cache_hard_ttl_hours": positive(8760),
	"fetch_lookback_window_days": positive(3650), "read_window_skew_minutes": positive(1440), "code_read_limit": positive(100), "purchase_read_limit": positive(500), "message_scan_limit": positive(500),
	"projection_replay_limit": positive(1000), "pickup_fetch_reserve_ttl_minutes": positive(30), "pickup_fetch_lease_ttl_minutes": positive(10), "pickup_message_cache_ttl_seconds": positive(300),
	"pickup_message_cache_limit": positive(100), "pickup_fetch_heartbeat_seconds": positive(300), "mailmatch_fetch_timeout_minutes": positive(60), "pickup_request_fetch_timeout_minutes": positive(30),
	"project_history_timeout_minutes": positive(120), "fetch_dispatcher_interval_seconds": positive(3600), "project_history_concurrency": positive(32), "project_history_dispatch_limit": positive(100),
	"microsoft_alias_weekly_limit": positive(1000), "microsoft_alias_yearly_limit": positive(10000), "microsoft_alias_ensure_interval_hours": positive(720), "microsoft_alias_reconciliation_grace_hours": positive(720),
	"microsoft_alias_transient_backoff_base_minutes": positive(1440), "microsoft_alias_transient_backoff_max_hours": positive(720), "microsoft_alias_negative_confirm_required": positive(20),
	"token_refresh_max_attempts": positive(20), "token_refresh_scan_limit": positive(10000), "token_refresh_lookahead_days": positive(365), "recovery_code_lease_minutes": positive(60),
	"password_recovery_code_wait_seconds": positive(1800), "msacl_token_poll_timeout_seconds": positive(1800), "msacl_token_poll_interval_seconds": positive(300),
	"imap_operation_timeout_seconds": positive(600), "imap_full_history_timeout_minutes": positive(120), "proxy_handshake_timeout_seconds": positive(120), "graph_message_page_top": positive(1000),
	"mail_stream_batch_size": positive(1000), "mail_fetch_client_timeout_seconds": positive(300), "imap_dial_timeout_seconds": positive(120), "imap_keepalive_seconds": positive(600), "oauth_validation_timeout_seconds": positive(300),
	"proxy_check_interval_seconds": positive(86400), "proxy_failure_threshold": positive(100), "proxy_check_timeout_seconds": positive(120), "resource_binding_ttl_days": positive(365), "max_proxy_attempts": positive(20),
	"pending_proxy_check_limit": positive(10000), "proxy_idle_conn_timeout_seconds": positive(600), "proxy_tls_handshake_timeout_seconds": positive(120),
	"smtp_outbound_payload_ttl_minutes": positive(1440), "outbound_mail_timeout_minutes": positive(120), "inbound_mail_timeout_minutes": positive(120),
	"auxiliary_domain_refresh_interval_seconds": positive(86400), "max_inbound_header_runes": positive(10000), "max_inbound_preview_runes": positive(10000), "max_inbound_body_bytes": positive(100 << 20),
	"max_inbound_body_runes": positive(1_000_000), "max_inbound_mime_depth": positive(50), "mail_dispatcher_interval_seconds": positive(3600), "alias_dispatcher_interval_seconds": positive(3600),
	"token_refresh_dispatcher_interval_seconds": positive(3600), "legacy_alias_retry_delay_seconds": positive(3600),
	"smtp_task_retry_count": {min: 0, max: 20},

	"background_load_overload_percent": {min: 11, max: 100}, "background_worker_minimum": positive(4096), "background_worker_initial": positive(4096),
	"background_worker_increase_step": positive(4096), "background_recovery_samples": positive(100), "background_metric_failure_limit": positive(100),
	"background_task_max_retry": {min: 0, max: 20}, "background_retry_delay_minimum_seconds": positive(3600), "background_retry_delay_jitter_seconds": {min: 0, max: 3600},
	"asynq_worker_concurrency": positive(4096), "asynq_realtime_worker_concurrency": positive(4096), "asynq_background_worker_concurrency": positive(4096),
	"asynq_shutdown_timeout_seconds": positive(300), "validation_dispatch_maximum": positive(10000), "default_inbound_smtp_max_connections": positive(10000),

	"admin_resource_bulk_max_ids": positive(1000), "admin_domain_bulk_max_ids": positive(1000), "admin_domain_bulk_max_filter": positive(10000),
	"resource_validation_max_ids": positive(10000), "validation_batch_page_size": positive(10000), "validation_insert_chunk_size": positive(10000),
	"bulk_user_chunk_size": positive(10000), "card_bulk_chunk_size": positive(10000), "retention_batch_size": positive(100000),
	"retention_batch_sleep_ms": {min: 0, max: 60000}, "retention_daily_run_hour": {min: 0, max: 23},
	"idempotency_key_retain_days": positive(3650), "mailmatch_ms_retain_days": positive(3650), "mailmatch_domain_retain_days": positive(3650),
	"daily_usage_retain_days": positive(3650), "outbound_mail_retain_days": positive(3650), "inbound_mail_retain_days": positive(3650), "system_log_retain_days": positive(3650),

	"admin_resource_list_default_limit": positive(100), "admin_log_default_limit": positive(100), "admin_task_default_limit": positive(100), "admin_ranking_limit": positive(100),
	"admin_message_default_limit": positive(100), "admin_message_max_search": positive(120),
	"dashboard_cache_ttl_hours": positive(8760), "leaderboard_cache_ttl_minutes": positive(1440), "ranking_refresh_interval_minutes": positive(1440),
	"resource_facets_cache_ttl_seconds": positive(3600),
	"ttl_cache_max_entries":             positive(1_000_000), "slow_request_threshold_ms": {min: 0, max: 600000}, "slow_sql_threshold_ms": {min: 0, max: 600000},
}

var removedKeys = map[string]struct{}{
	"admin_log_max_limit": {}, "admin_message_max_limit": {}, "admin_resource_list_max_limit": {},
	"admin_task_max_limit": {}, "api_key_cache_flush_interval_seconds": {}, "api_key_meta_ttl_seconds": {},
	"bucket_count": {}, "msacl_content_search_window_minutes": {}, "outbound_mail_claim_timeout_minutes": {},
}

func Validate(key, value string) error {
	key = canonicalKey(key)
	rawValue := value
	value = strings.TrimSpace(value)
	if _, removed := removedKeys[key]; removed {
		return domain.ErrInvalidKey
	}
	if limits, ok := integerRanges[key]; ok {
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number < limits.min || number > limits.max {
			return domain.ErrInvalidValue
		}
		return nil
	}
	switch key {
	case "token_refresh_hour":
		number, err := strconv.Atoi(value)
		if err != nil || number < 0 || number > 23 {
			return domain.ErrInvalidValue
		}
	case "verification_code_pattern":
		if value == "" || len(rawValue) > 4096 {
			return domain.ErrInvalidValue
		}
		if _, err := regexp.Compile(value); err != nil {
			return domain.ErrInvalidValue
		}
	case "microsoft_domain_whitelist":
		if value == "" {
			return nil
		}
		count := 0
		for _, candidate := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '，' || unicode.IsSpace(r) }) {
			count++
			candidate = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".")
			if !validDomain(candidate) {
				return domain.ErrInvalidValue
			}
		}
		if count == 0 {
			return domain.ErrInvalidValue
		}
	}
	return nil
}

// ValidateUpdates checks relationships after applying an atomic settings write
// to the current process snapshot.
func ValidateUpdates(settings []domain.Setting) error {
	return validateUpdates(clone(), settings)
}

// ValidatePersistedUpdates checks a write against the latest database snapshot.
// Callers use it while holding the settings rows inside the write transaction so
// relationship checks stay valid when multiple application replicas write.
func ValidatePersistedUpdates(persisted, updates []domain.Setting) error {
	values := make(map[string]string, len(persisted)+len(updates))
	for _, setting := range persisted {
		key := canonicalKey(setting.Key)
		if Validate(key, setting.Value) == nil {
			values[key] = strings.TrimSpace(setting.Value)
		}
	}
	sanitizeRelationships(values)
	return validateUpdates(values, updates)
}

func validateUpdates(values map[string]string, settings []domain.Setting) error {
	for _, setting := range settings {
		key := canonicalKey(setting.Key)
		if err := Validate(key, setting.Value); err != nil {
			return err
		}
		values[key] = strings.TrimSpace(setting.Value)
	}
	return validateRelationships(values)
}

func ValidateDelete(key string) error {
	key = canonicalKey(key)
	for _, setting := range defaultSettings {
		if setting.Key == key {
			return domain.ErrInvalidValue
		}
	}
	values := clone()
	delete(values, key)
	return validateRelationships(values)
}

func ValidateSnapshot(settings []domain.Setting) error {
	values := make(map[string]string, len(settings))
	for _, setting := range settings {
		key := canonicalKey(setting.Key)
		if err := Validate(key, setting.Value); err != nil {
			continue
		}
		values[key] = strings.TrimSpace(setting.Value)
	}
	return validateRelationships(values)
}

func sanitizeRelationships(values map[string]string) {
	drop := func(keys ...string) {
		for _, key := range keys {
			delete(values, key)
		}
	}
	value := func(key string, fallback int) int {
		number, err := strconv.Atoi(strings.TrimSpace(values[key]))
		if err != nil {
			return fallback
		}
		return number
	}
	if value("global_candidate_window", 8) < value("candidate_window_size", 4) {
		drop("candidate_window_size", "global_candidate_window")
	}
	if value("inventory_cache_hard_ttl_hours", 24)*60 < value("inventory_cache_activity_ttl_minutes", 20) {
		drop("inventory_cache_activity_ttl_minutes", "inventory_cache_hard_ttl_hours")
	}
	if value("message_scan_limit", 40) < max(value("code_read_limit", 1), value("purchase_read_limit", 30)) {
		drop("code_read_limit", "purchase_read_limit", "message_scan_limit")
	}
	if value("pickup_fetch_heartbeat_seconds", 30) > min(value("pickup_fetch_reserve_ttl_minutes", 2), value("pickup_fetch_lease_ttl_minutes", 2))*30 {
		drop("pickup_fetch_reserve_ttl_minutes", "pickup_fetch_lease_ttl_minutes", "pickup_fetch_heartbeat_seconds")
	}
	if value("microsoft_alias_weekly_limit", 2) > value("microsoft_alias_yearly_limit", 10) {
		drop("microsoft_alias_weekly_limit", "microsoft_alias_yearly_limit")
	}
	if value("microsoft_alias_transient_backoff_base_minutes", 15) > value("microsoft_alias_transient_backoff_max_hours", 12)*60 {
		drop("microsoft_alias_transient_backoff_base_minutes", "microsoft_alias_transient_backoff_max_hours")
	}
	if value("recovery_code_lease_minutes", 10)*60 < value("password_recovery_code_wait_seconds", 90)+30 {
		drop("recovery_code_lease_minutes", "password_recovery_code_wait_seconds")
	}
	if value("pickup_fetch_reserve_ttl_minutes", 2) > value("pickup_request_fetch_timeout_minutes", 2) {
		drop("pickup_fetch_reserve_ttl_minutes", "pickup_request_fetch_timeout_minutes")
	}
	if value("imap_full_history_timeout_minutes", 15) > value("project_history_timeout_minutes", 20) {
		drop("imap_full_history_timeout_minutes", "project_history_timeout_minutes")
	}
	if value("background_worker_minimum", 8) > value("background_worker_initial", 16) || value("background_worker_initial", 16) > value("asynq_background_worker_concurrency", 512) {
		drop("background_worker_minimum", "background_worker_initial", "asynq_background_worker_concurrency")
	}
	retries := value("smtp_task_retry_count", 3)
	if value("smtp_outbound_payload_ttl_minutes", 5) < value("outbound_mail_timeout_minutes", 3) || value("outbound_mail_timeout_minutes", 3)*60 < smtpTaskBudgetSeconds(retries) {
		drop("smtp_outbound_payload_ttl_minutes", "outbound_mail_timeout_minutes", "smtp_task_retry_count")
	}
}

func validateRelationships(values map[string]string) error {
	value := func(key string, fallback int) int {
		number, err := strconv.Atoi(strings.TrimSpace(values[key]))
		if err != nil {
			return fallback
		}
		return number
	}
	if value("global_candidate_window", 8) < value("candidate_window_size", 4) ||
		value("inventory_cache_hard_ttl_hours", 24)*60 < value("inventory_cache_activity_ttl_minutes", 20) ||
		value("message_scan_limit", 40) < max(value("code_read_limit", 1), value("purchase_read_limit", 30)) ||
		value("pickup_fetch_heartbeat_seconds", 30) > min(value("pickup_fetch_reserve_ttl_minutes", 2), value("pickup_fetch_lease_ttl_minutes", 2))*30 ||
		value("microsoft_alias_weekly_limit", 2) > value("microsoft_alias_yearly_limit", 10) ||
		value("microsoft_alias_transient_backoff_base_minutes", 15) > value("microsoft_alias_transient_backoff_max_hours", 12)*60 ||
		value("recovery_code_lease_minutes", 10)*60 < value("password_recovery_code_wait_seconds", 90)+30 ||
		value("pickup_fetch_reserve_ttl_minutes", 2) > value("pickup_request_fetch_timeout_minutes", 2) ||
		value("imap_full_history_timeout_minutes", 15) > value("project_history_timeout_minutes", 20) ||
		value("background_worker_minimum", 8) > value("background_worker_initial", 16) ||
		value("background_worker_initial", 16) > value("asynq_background_worker_concurrency", 512) ||
		value("smtp_outbound_payload_ttl_minutes", 5) < value("outbound_mail_timeout_minutes", 3) {
		return domain.ErrInvalidValue
	}
	retries := value("smtp_task_retry_count", 3)
	if value("outbound_mail_timeout_minutes", 3)*60 < smtpTaskBudgetSeconds(retries) {
		return domain.ErrInvalidValue
	}
	return nil
}

func smtpTaskBudgetSeconds(retries int) int {
	return (retries+1)*30 + retries*(retries+1)/2
}

func validDomain(value string) bool {
	if len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
