// Mock system settings API — returns fake data for all settings.
// Replace with real API calls when backend is ready.

export interface SystemOption { key: string; value: string; }

export function parseSettingsList<T>(raw: unknown): T[] {
  if (typeof raw !== "string" || !raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed as T[] : [];
  } catch {
    return [];
  }
}

export interface UserGroupFormValues {
  id?: number;
  code: string;
  name: string;
  description: string;
  enabled: boolean;
  api_rpm_limit: number;
  api_concurrency_limit: number;
  api_quota_limit: number;
  price_discount_ratio: number;
  topup_threshold: number;
  auto_upgrade_enabled: boolean;
}

const MOCK_OPTIONS: Record<string, string> = {
  system_name: "Remail", logo_url: "/logo.png", favicon_url: "", footer_text: "© Remail",
  announcements: "[]", global_notice: "", maintenance_notice: "",
  maintenance_mode: "false", maintenance_allow_ips: "", faq_enabled: "true", faq_list: "[]",
  register_enabled: "true",
  registration_email_whitelist: "qq.com,foxmail.com,gmail.com,proton.me,protonmail.com,pm.me,mail.com",
  registration_reward_amount: "0",
  password_login_enabled: "true", captcha_enabled: "true",
  github_oauth_enabled: "false", github_client_id: "", github_client_secret: "", github_callback_url: "",
  login_email_limit: "10", login_ip_limit: "60", login_window_seconds: "900",
  email_code_email_limit: "5", email_code_ip_limit: "30", email_code_window_seconds: "600",
  captcha_rate_limit: "30", email_code_ttl_seconds: "600", email_code_resend_gap_seconds: "60",
  email_code_digit_len: "6", bcrypt_cost: "12", session_max_age_seconds: "86400",
  microsoft_domain_whitelist: "outlook.com,hotmail.com,outlook.sa,outlook.com.ar,outlook.com.au,outlook.at,outlook.be,outlook.com.br,outlook.cl,outlook.cz,outlook.fr,outlook.de,outlook.com.gr,outlook.co.il,outlook.in,outlook.co.id,outlook.ie,outlook.it,outlook.hu,outlook.jp,outlook.kr,outlook.lv,outlook.my,outlook.co.nz,outlook.ph,outlook.pt,outlook.sg,outlook.sk,outlook.es,outlook.co.th,outlook.com.tr,outlook.com.vn",
  default_plus_daily_limit: "10000", default_mailbox_daily_limit: "10000",
  resource_validation_max_failures: "3", resource_import_max_bytes: "104857600",
  max_project_logo_bytes: "2097152", project_name_max: "120", project_description_max: "1000",
  project_target_platform_max: "120",
  bucket_count: "64", candidate_window_size: "4", global_candidate_window: "8",
  bucket_probe_count: "4", alias_generation_window: "32", candidate_retry_count: "5",
  dot_alias_capacity_per_resource: "10", inventory_refresh_interval_minutes: "10",
  inventory_cache_activity_ttl_minutes: "20", inventory_cache_hard_ttl_hours: "24",
  order_lifecycle_scanner_interval_seconds: "30", checkout_batch_concurrency: "1024",
  checkout_batch_max_waiting: "1024", checkout_batch_unit_size: "20",
  fetch_lookback_window_days: "90", read_window_skew_minutes: "2", code_read_limit: "1",
  purchase_read_limit: "30", message_scan_limit: "40", projection_replay_limit: "100",
  pickup_fetch_reserve_ttl_minutes: "2", pickup_fetch_lease_ttl_minutes: "2",
  pickup_message_cache_ttl_seconds: "10", pickup_message_cache_limit: "30",
  pickup_fetch_heartbeat_seconds: "30", mailmatch_fetch_timeout_minutes: "20",
  pickup_request_fetch_timeout_minutes: "2", project_history_timeout_minutes: "20",
  fetch_dispatcher_interval_seconds: "15", project_history_concurrency: "4",
  project_history_dispatch_limit: "4", verification_code_pattern: "(^|[^\\d])(\\d{6,8})([^\\d]|$)",
  microsoft_alias_weekly_limit: "2", microsoft_alias_yearly_limit: "10",
  microsoft_alias_ensure_interval_hours: "24", microsoft_alias_reconciliation_grace_hours: "24",
  microsoft_alias_transient_backoff_base_minutes: "15", microsoft_alias_transient_backoff_max_hours: "12",
  microsoft_alias_negative_confirm_required: "3", token_refresh_max_attempts: "3",
  token_refresh_scan_limit: "2000", token_refresh_lookahead_days: "30", token_refresh_hour: "3",
  recovery_code_lease_minutes: "10", password_recovery_code_wait_seconds: "90",
  msacl_content_search_window_minutes: "10", msacl_token_poll_timeout_seconds: "15",
  msacl_token_poll_interval_seconds: "3", imap_operation_timeout_seconds: "60",
  imap_full_history_timeout_minutes: "15", proxy_handshake_timeout_seconds: "30",
  graph_message_page_top: "100", mail_stream_batch_size: "100",
  mail_fetch_client_timeout_seconds: "30", imap_dial_timeout_seconds: "20",
  imap_keepalive_seconds: "30", oauth_validation_timeout_seconds: "30",
  background_load_overload_percent: "50", background_worker_minimum: "8",
  background_worker_initial: "16", background_worker_increase_step: "8",
  background_recovery_samples: "2", background_metric_failure_limit: "3",
  background_task_max_retry: "5", background_retry_delay_minimum_seconds: "5",
  background_retry_delay_jitter_seconds: "5", asynq_worker_concurrency: "768",
  asynq_realtime_worker_concurrency: "256", asynq_background_worker_concurrency: "512",
  asynq_shutdown_timeout_seconds: "30", validation_dispatch_maximum: "128",
  default_inbound_smtp_max_connections: "200",
  admin_resource_bulk_max_ids: "1000", admin_domain_bulk_max_ids: "1000",
  admin_domain_bulk_max_filter: "10000", resource_validation_max_ids: "10000",
  validation_batch_page_size: "1000", validation_insert_chunk_size: "1000",
  bulk_user_chunk_size: "5000", card_bulk_chunk_size: "5000", retention_batch_size: "5000",
  retention_batch_sleep_ms: "200", retention_daily_run_hour: "4",
  idempotency_key_retain_days: "30", mailmatch_ms_retain_days: "3",
  mailmatch_domain_retain_days: "30", daily_usage_retain_days: "14",
  outbound_mail_retain_days: "30", inbound_mail_retain_days: "30", system_log_retain_days: "30",
  proxy_check_interval_seconds: "15", proxy_failure_threshold: "3",
  proxy_check_timeout_seconds: "6", resource_binding_ttl_days: "7", max_proxy_attempts: "3",
  pending_proxy_check_limit: "100", proxy_idle_conn_timeout_seconds: "15",
  proxy_tls_handshake_timeout_seconds: "5",
  outbound_mail_timeout_minutes: "3", inbound_mail_timeout_minutes: "2",
  outbound_mail_claim_timeout_minutes: "2", auxiliary_domain_refresh_interval_seconds: "60",
  max_inbound_header_runes: "500", max_inbound_preview_runes: "1000",
  max_inbound_body_bytes: "1048576", max_inbound_body_runes: "200000", max_inbound_mime_depth: "12",
  mail_dispatcher_interval_seconds: "15", alias_dispatcher_interval_seconds: "2",
  token_refresh_dispatcher_interval_seconds: "2", legacy_alias_retry_delay_seconds: "30",
  epay_version: "v1", epay_gateway_url: "", epay_merchant_id: "", epay_merchant_key: "",
  epay_notify_url: "", epay_return_url: "", epay_custom_callback_domain: "",
  min_topup_amount: "10", topup_fee_rate: "0", topup_fee_cap: "0",
  topup_amount_presets: "[10, 20, 50, 100, 200, 500]", topup_amount_bonus: "{}",
  async_check_enabled: "true", async_check_poll_interval_seconds: "30",
  async_check_max_retries: "10", async_check_timeout_minutes: "30",
  async_check_request_timeout_seconds: "5",
  first_order_rebate_ratio: "0.8", single_rebate_cap: "0", cumulative_rebate_cap: "0",
  rebate_expiry_days: "90",
  admin_resource_list_default_limit: "20", admin_resource_list_max_limit: "100",
  admin_log_default_limit: "20", admin_log_max_limit: "100", admin_task_default_limit: "20",
  admin_task_max_limit: "100", admin_ranking_limit: "10", admin_message_default_limit: "20",
  admin_message_max_limit: "100", admin_message_max_search: "120",
  dashboard_cache_ttl_hours: "24", leaderboard_cache_ttl_minutes: "15",
  ranking_refresh_interval_minutes: "5",
  api_key_meta_ttl_seconds: "30", api_key_cache_flush_interval_seconds: "5",
  resource_facets_cache_ttl_seconds: "10", ttl_cache_max_entries: "4096",
  slow_request_threshold_ms: "1000", slow_sql_threshold_ms: "200",
};

const MOCK_GROUPS: UserGroupFormValues[] = [
  { id: 1, code: "normal", name: "普通用户", description: "默认注册分组", enabled: true, api_rpm_limit: 60, api_concurrency_limit: 3, api_quota_limit: 10000, price_discount_ratio: 1.0, topup_threshold: 0, auto_upgrade_enabled: false },
  { id: 2, code: "vip", name: "VIP会员", description: "VIP专享", enabled: true, api_rpm_limit: 300, api_concurrency_limit: 10, api_quota_limit: 100000, price_discount_ratio: 0.9, topup_threshold: 100, auto_upgrade_enabled: true },
  { id: 3, code: "enterprise", name: "企业级", description: "最高限额", enabled: true, api_rpm_limit: 600, api_concurrency_limit: 30, api_quota_limit: 500000, price_discount_ratio: 0.8, topup_threshold: 500, auto_upgrade_enabled: true },
];

let store = { ...MOCK_OPTIONS };
let groups = [...MOCK_GROUPS];
let nextId = 4;

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export async function getSystemOptions(): Promise<{ options: SystemOption[] }> {
  await sleep(100);
  return { options: Object.entries(store).map(([key, value]) => ({ key, value })) };
}

export async function updateSystemOption(key: string, value: string): Promise<{ success: boolean }> {
  await sleep(200);
  store[key] = String(value);
  return { success: true };
}

export async function updateSystemOptionsBulk(updates: { key: string; value: string }[]): Promise<{ success: boolean }> {
  await sleep(200);
  for (const u of updates) store[u.key] = String(u.value);
  return { success: true };
}

export async function getUserGroups(): Promise<{ groups: UserGroupFormValues[] }> {
  await sleep(100);
  return { groups: [...groups] };
}

export async function createUserGroup(data: Omit<UserGroupFormValues, "id">): Promise<{ group: UserGroupFormValues }> {
  await sleep(200);
  const g: UserGroupFormValues = { ...data, id: nextId++ };
  groups.push(g);
  return { group: g };
}

export async function updateUserGroup(groupId: number, data: Partial<UserGroupFormValues>): Promise<{ group: UserGroupFormValues }> {
  await sleep(200);
  const idx = groups.findIndex((g) => g.id === groupId);
  if (idx < 0) throw new Error("Group not found");
  groups[idx] = { ...groups[idx], ...data };
  return { group: groups[idx] };
}

export function parseOption<T extends Record<string, unknown>>(options: SystemOption[] | undefined, defaults: T): T {
  if (!options?.length) return defaults;
  const result = { ...defaults };
  const map = new Map(options.map((o) => [o.key, o.value]));
  for (const key of Object.keys(defaults)) {
    const raw = map.get(key);
    if (raw === undefined) continue;
    const def = defaults[key];
    if (typeof def === "boolean") { (result as any)[key] = raw === "true" || raw === "1"; }
    else if (typeof def === "number") { const n = Number(raw); (result as any)[key] = Number.isFinite(n) ? n : def; }
    else { (result as any)[key] = raw; }
  }
  return result;
}
