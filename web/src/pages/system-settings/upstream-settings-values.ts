import { listProjects, type ProjectItem, type ProjectListResponse } from "@/lib/projects-api";

export const SMSBOWER_UPSTREAM_KEYS = [
  "smsbower_enabled",
  "smsbower_code_enabled",
  "smsbower_purchase_enabled",
  "smsbower_api_key",
  "smsbower_sync_interval_minutes",
  "smsbower_balance_warning_threshold",
  "smsbower_points_per_unit",
  "smsbower_min_margin_rate",
] as const;

export function buildSMSBowerSettingsUpdates(form: {
  smsbower_api_key: string;
  smsbower_balance_warning_threshold: number;
  smsbower_code_enabled: boolean;
  smsbower_enabled: boolean;
  smsbower_min_margin_percent: number;
  smsbower_points_per_unit: number;
  smsbower_purchase_enabled: boolean;
  smsbower_sync_interval_minutes: number;
}, canSensitive: boolean) {
  const updates = [
    { key: "smsbower_enabled", value: String(form.smsbower_enabled) },
    { key: "smsbower_code_enabled", value: String(form.smsbower_code_enabled) },
    { key: "smsbower_purchase_enabled", value: String(form.smsbower_purchase_enabled) },
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

type ProjectPageLoader = (offset: number, limit: number) => Promise<ProjectListResponse>;

export async function loadAllProjects(
  fetchPage: ProjectPageLoader = (offset, limit) =>
    listProjects({ scope: "all" }, offset, limit),
): Promise<ProjectItem[]> {
  const items: ProjectItem[] = [];
  const limit = 100;
  while (true) {
    const page = await fetchPage(items.length, limit);
    items.push(...page.items);
    if (items.length >= page.total || page.items.length === 0) return items;
  }
}
