package runtimeconfig

import (
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/shopspring/decimal"
)

const (
	maxSystemNoticeBytes        = 1 << 20
	maxVerificationPatternBytes = 4096
	maxVerificationPatternCount = 64
)

type integerRange struct {
	min int64
	max int64
}

func positive(maximum int64) integerRange { return integerRange{min: 1, max: maximum} }

var integerRanges = map[string]integerRange{
	"login_email_limit": positive(1000), "login_ip_limit": positive(10000), "login_window_seconds": positive(86400),
	"email_code_email_limit": positive(1000), "email_code_ip_limit": positive(10000), "email_code_window_seconds": positive(86400), "captcha_rate_limit": positive(10000),
	"email_code_ttl_seconds": positive(86400), "email_code_resend_gap_seconds": positive(3600), "email_code_digit_len": {min: 4, max: 10},
	"bcrypt_cost": {min: 4, max: 16}, "session_max_age_seconds": {min: 300, max: 31_536_000},
	"linuxdo_minimum_trust_level":         {min: 0, max: 4},
	"github_minimum_account_age_days":     {min: 0, max: 36500},
	"rebate_expiry_days":                  {min: 0, max: 36500},
	"max_pending_recharge_orders":         positive(100),
	"async_check_request_timeout_seconds": {min: 1, max: 30},

	"domain_max_subdomains_per_registrable_domain": positive(1000), "default_plus_daily_limit": positive(2_147_483_647), "default_mailbox_daily_limit": positive(2_147_483_647), "resource_validation_max_failures": positive(100),
	"resource_import_max_bytes": positive(512 << 20), "max_project_logo_bytes": positive(20 << 20), "project_name_max": positive(120), "project_description_max": positive(1000), "project_target_platform_max": positive(120),
	"candidate_window_size": positive(100), "global_candidate_window": positive(100), "bucket_probe_count": positive(64), "alias_generation_window": positive(1000),
	"candidate_retry_count": positive(20), "dot_alias_capacity_per_resource": positive(64), "inventory_refresh_interval_minutes": positive(1440), "inventory_cache_hard_ttl_hours": positive(8760),
	"fetch_lookback_window_days": positive(3650), "read_window_skew_minutes": positive(1440), "code_read_limit": positive(100), "purchase_read_limit": positive(500),
	"pickup_fetch_reserve_ttl_minutes": positive(30), "pickup_fetch_lease_ttl_minutes": positive(10), "pickup_message_cache_ttl_seconds": positive(300),
	"pickup_message_cache_limit": positive(100), "pickup_fetch_heartbeat_seconds": positive(300), "mailmatch_fetch_timeout_minutes": positive(60), "pickup_request_fetch_timeout_minutes": positive(30),
	"project_history_timeout_minutes": positive(120), "fetch_dispatcher_interval_seconds": positive(3600), "fetch_dispatcher_timeout_seconds": positive(3600), "resource_fetch_dispatch_limit": positive(10000), "project_history_concurrency": positive(8096), "gmail_history_concurrency": positive(8096), "project_history_dispatch_limit": positive(100),
	"microsoft_alias_weekly_limit": positive(1000), "microsoft_alias_yearly_limit": positive(10000), "microsoft_alias_ensure_interval_hours": positive(720), "microsoft_alias_reconciliation_grace_hours": positive(720),
	"microsoft_alias_transient_backoff_base_minutes": positive(1440), "microsoft_alias_transient_backoff_max_hours": positive(720), "microsoft_alias_negative_confirm_required": positive(20),
	"token_refresh_max_attempts": positive(20), "token_refresh_scan_limit": positive(10000), "token_refresh_lookahead_days": positive(365), "recovery_code_lease_minutes": positive(60),
	"password_recovery_code_wait_seconds": positive(1800), "msacl_token_poll_timeout_seconds": positive(1800), "msacl_token_poll_interval_seconds": positive(300),
	"imap_operation_timeout_seconds": positive(600), "imap_full_history_timeout_minutes": positive(120), "proxy_handshake_timeout_seconds": positive(120), "graph_message_page_top": positive(1000),
	"mail_stream_batch_size": positive(1000), "mail_fetch_client_timeout_seconds": positive(300), "imap_dial_timeout_seconds": positive(120), "imap_keepalive_seconds": positive(600), "oauth_validation_timeout_seconds": positive(300),
	"proxy_check_interval_seconds": positive(86400), "proxy_failure_threshold": positive(100), "proxy_check_timeout_seconds": positive(120), "resource_binding_ttl_days": positive(365), "max_proxy_attempts": positive(20),
	"pending_proxy_check_limit": positive(10000), "proxy_idle_conn_timeout_seconds": positive(600), "proxy_tls_handshake_timeout_seconds": positive(120),
	"proxy_server_health_timeout_seconds": {min: 1, max: 10}, "proxy_server_health_interval_seconds": positive(86400), "proxy_server_health_dispatch_lease_seconds": {min: 15, max: 3600},
	"proxy_server_failure_threshold": positive(100), "proxy_server_inventory_threshold_percent": {min: 1, max: 100},
	"smtp_outbound_payload_ttl_minutes": positive(1440), "outbound_mail_timeout_minutes": positive(120), "inbound_mail_timeout_minutes": positive(120),
	"auxiliary_domain_refresh_interval_seconds": positive(86400), "max_inbound_header_runes": positive(10000), "max_inbound_preview_runes": positive(10000), "max_inbound_body_bytes": positive(100 << 20),
	"max_inbound_body_runes": positive(1_000_000), "max_inbound_mime_depth": positive(50), "mail_dispatcher_interval_seconds": positive(3600), "alias_dispatcher_interval_seconds": positive(3600),
	"token_refresh_dispatcher_interval_seconds": positive(3600), "legacy_alias_retry_delay_seconds": positive(3600),
	"smtp_task_retry_count": {min: 0, max: 20},

	"background_load_overload_percent": {min: 11, max: 100}, "background_worker_minimum": positive(8096), "background_worker_initial": positive(8096),
	"background_worker_increase_step": positive(8096), "background_recovery_samples": positive(100), "background_metric_failure_limit": positive(100),
	"background_task_max_retry": {min: 0, max: 20}, "background_retry_delay_minimum_seconds": positive(3600), "background_retry_delay_jitter_seconds": {min: 0, max: 3600},
	"asynq_worker_concurrency": positive(8096), "asynq_realtime_worker_concurrency": positive(8096), "asynq_background_worker_concurrency": positive(8096),
	"asynq_queue_mailfetch_weight": positive(10000), "asynq_queue_payment_reconcile_weight": positive(10000), "asynq_queue_mailtransport_weight": positive(10000), "asynq_queue_default_weight": positive(10000),
	"asynq_queue_background_validation_weight": positive(10000), "asynq_queue_background_domain_validation_weight": positive(10000), "asynq_queue_background_alias_weight": positive(10000),
	"asynq_queue_background_token_refresh_weight": positive(10000), "asynq_queue_resource_weight": positive(10000), "asynq_queue_background_project_history_weight": positive(10000), "asynq_queue_background_inventory_weight": positive(10000),
	"asynq_shutdown_timeout_seconds": positive(300), "validation_dispatch_maximum": positive(10000), "default_inbound_smtp_max_connections": positive(10000),

	"admin_resource_bulk_max_ids": positive(1000), "admin_domain_bulk_max_ids": positive(1000), "admin_domain_bulk_max_filter": positive(10000),
	"resource_validation_max_ids": positive(10000), "validation_batch_page_size": positive(10000), "validation_insert_chunk_size": positive(10000),
	"bulk_user_chunk_size": positive(10000), "card_bulk_chunk_size": positive(10000), "retention_batch_size": positive(100000),
	"retention_batch_sleep_ms": {min: 0, max: 60000}, "retention_daily_run_hour": {min: 0, max: 23},
	"idempotency_key_retain_days": positive(3650), "mailmatch_ms_retain_days": positive(3650), "mailmatch_domain_retain_days": positive(3650), "gmail_code_retain_days": positive(3650),
	"daily_usage_retain_days": positive(3650), "outbound_mail_retain_days": positive(3650), "inbound_mail_retain_days": positive(3650), "system_log_retain_days": positive(3650),

	"admin_resource_list_default_limit": positive(100), "admin_log_default_limit": positive(100), "admin_task_default_limit": positive(100), "admin_ranking_limit": positive(100),
	"admin_message_default_limit": positive(100), "admin_message_max_search": positive(120),
	"dashboard_cache_ttl_hours": positive(8760), "leaderboard_cache_ttl_minutes": positive(1440), "ranking_refresh_interval_minutes": positive(1440),
	"resource_facets_cache_ttl_seconds":       positive(3600),
	"admin_resource_facets_cache_ttl_seconds": positive(3600),
	"ttl_cache_max_entries":                   positive(1_000_000), "slow_request_threshold_ms": {min: 0, max: 600000}, "slow_sql_threshold_ms": {min: 0, max: 600000},
}

var removedKeys = map[string]struct{}{
	"admin_log_max_limit": {}, "admin_message_max_limit": {}, "admin_resource_list_max_limit": {},
	"admin_task_max_limit": {}, "api_key_cache_flush_interval_seconds": {}, "api_key_meta_ttl_seconds": {},
	"bucket_count": {}, "msacl_content_search_window_minutes": {}, "outbound_mail_claim_timeout_minutes": {},
	"inventory_cache_activity_ttl_minutes": {}, "message_scan_limit": {}, "projection_replay_limit": {},
	"smsbower_enabled": {}, "smsbower_code_enabled": {}, "smsbower_purchase_enabled": {}, "smsbower_api_key": {},
	"smsbower_sync_interval_minutes": {}, "smsbower_balance_warning_threshold": {}, "smsbower_points_per_unit": {}, "smsbower_min_margin_rate": {},
}

var booleanKeys = map[string]struct{}{
	"register_enabled": {}, "captcha_enabled": {}, "announcement_enabled": {}, "faq_enabled": {}, "epay_enabled": {},
	"daily_checkin_enabled": {}, "leaderboard_reward_enabled": {}, "linuxdo_oauth_enabled": {}, "github_oauth_enabled": {},
}

func Validate(key, value string) error {
	key = canonicalKey(key)
	rawValue := value
	value = strings.TrimSpace(value)
	if _, removed := removedKeys[key]; removed {
		return domain.ErrInvalidKey
	}
	if _, ok := booleanKeys[key]; ok {
		if value != "true" && value != "false" {
			return domain.ErrInvalidValue
		}
		return nil
	}
	if limits, ok := integerRanges[key]; ok {
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number < limits.min || number > limits.max {
			return domain.ErrInvalidValue
		}
		return nil
	}
	switch key {
	case "announcements":
		return validateAnnouncements(rawValue)
	case "faq_list":
		return validateFAQs(rawValue)
	case "global_notice":
		if len(rawValue) > maxSystemNoticeBytes {
			return domain.ErrInvalidValue
		}
	case "registration_reward_amount", "single_rebate_cap", "cumulative_rebate_cap", "topup_fee_cap",
		"default_project_microsoft_code_price", "default_project_microsoft_code_supplier_price",
		"default_project_microsoft_purchase_price", "default_project_microsoft_purchase_supplier_price",
		"default_project_domain_code_price", "default_project_domain_code_supplier_price",
		"default_project_domain_purchase_price", "default_project_domain_purchase_supplier_price",
		"default_project_gmail_code_price", "default_project_gmail_code_supplier_price",
		"default_project_gmail_purchase_price", "default_project_gmail_purchase_supplier_price":
		amount, err := money.Parse(value)
		if err != nil || amount.IsNegative() {
			return domain.ErrInvalidValue
		}
	case "min_topup_amount", "points_per_yuan":
		amount, err := money.Parse(value)
		if err != nil || !amount.IsPositive() {
			return domain.ErrInvalidValue
		}
	case "points_unit_migration_v1":
		if value != "completed" {
			return domain.ErrInvalidValue
		}
	case "topup_fee_rate":
		rate, err := money.Parse(value)
		if err != nil || rate.IsNegative() || rate.GreaterThan(decimal.NewFromInt(100)) {
			return domain.ErrInvalidValue
		}
	case "first_order_rebate_ratio":
		ratio, err := money.Parse(value)
		if err != nil || ratio.IsNegative() || ratio.GreaterThan(decimal.NewFromInt(1)) {
			return domain.ErrInvalidValue
		}
	case "token_refresh_hour":
		number, err := strconv.Atoi(value)
		if err != nil || number < 0 || number > 23 {
			return domain.ErrInvalidValue
		}
	case "verification_code_pattern":
		return validateVerificationPatterns(rawValue)
	case "microsoft_domain_whitelist", "registration_email_whitelist", "domain_custom_tlds":
		if value == "" {
			return nil
		}
		count := 0
		for _, candidate := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '，' || unicode.IsSpace(r) }) {
			count++
			candidate = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".")
			if !validDomain(candidate) || key == "domain_custom_tlds" && len(candidate) > 63 {
				return domain.ErrInvalidValue
			}
		}
		if count == 0 {
			return domain.ErrInvalidValue
		}
	case "epay_version":
		if value != "v1" && value != "v2" {
			return domain.ErrInvalidValue
		}
	case "epay_gateway_url", "epay_notify_url", "epay_return_url", "redemption_code_purchase_url":
		if value == "" {
			return nil
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return domain.ErrInvalidValue
		}
	case "linuxdo_callback_url", "github_callback_url":
		if value == "" {
			return nil
		}
		callbackPath := "/v1/oauth/linuxdo/callback"
		if key == "github_callback_url" {
			callbackPath = "/v1/oauth/github/callback"
		}
		if rawValue != value || !validOAuthCallbackURL(value, callbackPath) {
			return domain.ErrInvalidValue
		}
	case "linuxdo_client_id", "linuxdo_client_secret", "github_client_id", "github_client_secret":
		if rawValue != value || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return domain.ErrInvalidValue
		}
	case "epay_merchant_id", "epay_merchant_key":
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return domain.ErrInvalidValue
		}
	case "epay_private_key", "epay_platform_public_key":
		if len(rawValue) > 32<<10 || strings.ContainsRune(rawValue, '\x00') {
			return domain.ErrInvalidValue
		}
	case "topup_amount_presets":
		var values []json.Number
		if err := json.Unmarshal([]byte(value), &values); err != nil || len(values) == 0 || len(values) > 100 {
			return domain.ErrInvalidValue
		}
		seen := make(map[string]struct{}, len(values))
		for _, raw := range values {
			amount, err := money.Parse(string(raw))
			if err != nil || !amount.IsPositive() || !amount.Equal(amount.Truncate(0)) {
				return domain.ErrInvalidValue
			}
			normalized := money.Format(amount)
			if _, exists := seen[normalized]; exists {
				return domain.ErrInvalidValue
			}
			seen[normalized] = struct{}{}
		}
	case "topup_amount_bonus":
		var values map[string]json.Number
		if err := json.Unmarshal([]byte(value), &values); err != nil || values == nil || len(values) > 100 {
			return domain.ErrInvalidValue
		}
		for rawAmount, rawBonus := range values {
			amount, amountErr := money.Parse(rawAmount)
			bonus, bonusErr := money.Parse(string(rawBonus))
			if amountErr != nil || !amount.IsPositive() || !amount.Equal(amount.Truncate(0)) || bonusErr != nil || bonus.IsNegative() {
				return domain.ErrInvalidValue
			}
		}
	case "daily_checkin_reward_rules":
		if _, err := ParseCheckinRewardRules(value); err != nil {
			return domain.ErrInvalidValue
		}
	case "leaderboard_reward_rules":
		if _, err := ParseLeaderboardRewardRules(value); err != nil {
			return domain.ErrInvalidValue
		}
	case "leaderboard_settlement_time":
		if _, _, err := ParseSettlementClock(value); err != nil {
			return domain.ErrInvalidValue
		}
	}
	return nil
}

func validateVerificationPatterns(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" || len(raw) > maxVerificationPatternBytes {
		return domain.ErrInvalidValue
	}
	var patterns []string
	if json.Unmarshal([]byte(value), &patterns) == nil {
		if len(patterns) == 0 || len(patterns) > maxVerificationPatternCount {
			return domain.ErrInvalidValue
		}
		for _, pattern := range patterns {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				return domain.ErrInvalidValue
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return domain.ErrInvalidValue
			}
		}
		return nil
	}
	if _, err := regexp.Compile(value); err != nil {
		return domain.ErrInvalidValue
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
	if key == "points_unit_migration_v1" {
		return domain.ErrInvalidValue
	}
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
	if strings.TrimSpace(values["epay_enabled"]) == "true" && len(invalidEPayConfigFields(values)) > 0 {
		values["epay_enabled"] = "false"
	}
	if strings.TrimSpace(values["linuxdo_oauth_enabled"]) == "true" && len(invalidLinuxDOConfigFields(values)) > 0 {
		values["linuxdo_oauth_enabled"] = "false"
	}
	if strings.TrimSpace(values["github_oauth_enabled"]) == "true" && len(invalidGitHubConfigFields(values)) > 0 {
		values["github_oauth_enabled"] = "false"
	}
	if strings.TrimSpace(values["daily_checkin_enabled"]) == "true" {
		if rules, err := ParseCheckinRewardRules(values["daily_checkin_reward_rules"]); err != nil || len(rules) == 0 {
			values["daily_checkin_enabled"] = "false"
		}
	}
	if strings.TrimSpace(values["leaderboard_reward_enabled"]) == "true" {
		if rules, err := ParseLeaderboardRewardRules(values["leaderboard_reward_rules"]); err != nil || len(rules) == 0 {
			values["leaderboard_reward_enabled"] = "false"
		}
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
	if strings.TrimSpace(values["epay_enabled"]) == "true" {
		if fields := invalidEPayConfigFields(values); len(fields) > 0 {
			return &domain.InvalidValueFieldsError{Fields: fields}
		}
	}
	if strings.TrimSpace(values["linuxdo_oauth_enabled"]) == "true" {
		if fields := invalidLinuxDOConfigFields(values); len(fields) > 0 {
			return &domain.InvalidValueFieldsError{Fields: fields}
		}
	}
	if strings.TrimSpace(values["github_oauth_enabled"]) == "true" {
		if fields := invalidGitHubConfigFields(values); len(fields) > 0 {
			return &domain.InvalidValueFieldsError{Fields: fields}
		}
	}
	if strings.TrimSpace(values["daily_checkin_enabled"]) == "true" {
		if rules, err := ParseCheckinRewardRules(values["daily_checkin_reward_rules"]); err != nil || len(rules) == 0 {
			return domain.ErrInvalidValue
		}
	}
	if strings.TrimSpace(values["leaderboard_reward_enabled"]) == "true" {
		if rules, err := ParseLeaderboardRewardRules(values["leaderboard_reward_rules"]); err != nil || len(rules) == 0 {
			return domain.ErrInvalidValue
		}
	}
	return nil
}

func invalidEPayConfigFields(values map[string]string) map[string]string {
	fields := make(map[string]string)
	require := func(key string) {
		if strings.TrimSpace(values[key]) == "" {
			fields[key] = "Required when EPay is enabled."
		} else if Validate(key, values[key]) != nil {
			fields[key] = "Invalid value."
		}
	}
	for _, key := range []string{"epay_gateway_url", "epay_merchant_id", "epay_notify_url", "epay_return_url"} {
		require(key)
	}
	version := strings.TrimSpace(values["epay_version"])
	switch version {
	case "v2":
		for _, key := range []string{"epay_private_key", "epay_platform_public_key"} {
			require(key)
		}
	case "v1":
		require("epay_merchant_key")
	default:
		fields["epay_version"] = "Invalid value."
	}
	return fields
}

func invalidLinuxDOConfigFields(values map[string]string) map[string]string {
	fields := make(map[string]string)
	for _, key := range []string{"linuxdo_client_id", "linuxdo_client_secret", "linuxdo_callback_url"} {
		if strings.TrimSpace(values[key]) == "" {
			fields[key] = "Required when LinuxDO OAuth is enabled."
		} else if Validate(key, values[key]) != nil {
			fields[key] = "Invalid value."
		}
	}
	return fields
}

func invalidGitHubConfigFields(values map[string]string) map[string]string {
	fields := make(map[string]string)
	for _, key := range []string{"github_client_id", "github_client_secret", "github_callback_url"} {
		if strings.TrimSpace(values[key]) == "" {
			fields[key] = "Required when GitHub OAuth is enabled."
		} else if Validate(key, values[key]) != nil {
			fields[key] = "Invalid value."
		}
	}
	return fields
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

func validOAuthCallbackURL(value, callbackPath string) bool {
	parsed, err := url.Parse(value)
	if len(value) > 2048 || err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != callbackPath {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
