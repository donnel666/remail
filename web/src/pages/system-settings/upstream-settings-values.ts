export const SMSBOWER_UPSTREAM_KEYS = [
  "smsbower_enabled",
  "smsbower_api_key",
  "smsbower_sync_interval_minutes",
  "smsbower_balance_warning_threshold",
  "smsbower_points_per_unit",
  "smsbower_min_margin_rate",
] as const;

export function buildSMSBowerSettingsUpdates(form: {
  smsbower_api_key: string;
  smsbower_balance_warning_threshold: number;
  smsbower_enabled: boolean;
  smsbower_min_margin_percent: number;
  smsbower_points_per_unit: number;
  smsbower_sync_interval_minutes: number;
}, canSensitive: boolean) {
  const updates = [
    { key: "smsbower_enabled", value: String(form.smsbower_enabled) },
    { key: "smsbower_sync_interval_minutes", value: String(form.smsbower_sync_interval_minutes) },
    { key: "smsbower_balance_warning_threshold", value: String(form.smsbower_balance_warning_threshold) },
    { key: "smsbower_points_per_unit", value: String(form.smsbower_points_per_unit) },
    { key: "smsbower_min_margin_rate", value: String(form.smsbower_min_margin_percent / 100) },
  ];
  if (canSensitive && form.smsbower_api_key.trim()) {
    updates.push({ key: "smsbower_api_key", value: form.smsbower_api_key.trim() });
  }
  return updates;
}
