import { apiClient, csrfHeader, unwrap } from "./api-client";
import type { components } from "./openapi/schema";

export interface SystemOption {
  key: string;
  value: string;
}

export function parseSettingsList<T>(raw: unknown): T[] {
  if (typeof raw !== "string" || !raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? (parsed as T[]) : [];
  } catch {
    return [];
  }
}

export function parseOption<T extends Record<string, unknown>>(
  options: SystemOption[] | undefined,
  defaults: T,
): T {
  if (!options?.length) return defaults;
  const result = { ...defaults };
  const values = new Map(options.map((option) => [option.key, option.value]));
  for (const key of Object.keys(defaults)) {
    const raw = values.get(key);
    if (raw === undefined) continue;
    const fallback = defaults[key];
    if (typeof fallback === "boolean") {
      (result as Record<string, unknown>)[key] = raw === "true" || raw === "1";
    } else if (typeof fallback === "number") {
      const value = Number(raw);
      (result as Record<string, unknown>)[key] = Number.isFinite(value) ? value : fallback;
    } else {
      (result as Record<string, unknown>)[key] = raw;
    }
  }
  return result;
}

export async function getSystemOptions(): Promise<{ options: SystemOption[] }> {
  const response = await unwrap<components["schemas"]["AdminSystemSettingsResponse"]>(
    await apiClient.GET("/v1/admin/settings"),
  );
  return { options: response.options };
}

export async function updateSystemOption(key: string, value: string): Promise<void> {
  await unwrap<components["schemas"]["AdminSystemSettingResponse"]>(await apiClient.PUT("/v1/admin/settings/{key}", {
    params: { header: csrfHeader(), path: { key } },
    body: { value },
  }));
}

export async function updateSystemOptionsBulk(
  updates: SystemOption[],
): Promise<void> {
  await unwrap<components["schemas"]["AdminSystemSettingsResponse"]>(await apiClient.PUT("/v1/admin/settings", {
    params: { header: csrfHeader() },
    body: { settings: updates },
  }));
}

export interface UserGroupFormValues {
  id?: number;
  code: string;
  name: string;
  description: string;
  enabled: boolean;
  // Kept for the settings form while group capabilities are introduced by
  // the IAM API. They are not sent to the current group endpoint.
  api_rpm_limit: number;
  api_concurrency_limit: number;
  api_quota_limit: number;
  price_discount_ratio: number;
  topup_threshold: number;
  auto_upgrade_enabled: boolean;
}

type UserGroupResponse = Omit<UserGroupFormValues, "api_rpm_limit" | "api_concurrency_limit" | "api_quota_limit" | "price_discount_ratio" | "topup_threshold" | "auto_upgrade_enabled">;

function withGroupDefaults(group: UserGroupResponse): UserGroupFormValues {
  return {
    ...group,
    api_rpm_limit: 0,
    api_concurrency_limit: 0,
    api_quota_limit: 0,
    price_discount_ratio: 1,
    topup_threshold: 0,
    auto_upgrade_enabled: false,
  };
}

export async function getUserGroups(): Promise<{ groups: UserGroupFormValues[] }> {
  const response = await unwrap<{ groups: UserGroupResponse[] }>(await apiClient.GET("/v1/admin/users/groups"));
  return { groups: response.groups.map(withGroupDefaults) };
}

export async function createUserGroup(data: Omit<UserGroupFormValues, "id">): Promise<{ group: UserGroupFormValues }> {
  const response = await unwrap<{ group: UserGroupResponse }>(await apiClient.POST("/v1/admin/users/groups" as never, {
    params: { header: csrfHeader() },
    body: { code: data.code, name: data.name, description: data.description, enabled: data.enabled },
  } as never));
  return { group: withGroupDefaults(response.group) };
}

export async function updateUserGroup(groupId: number, data: Partial<UserGroupFormValues>): Promise<{ group: UserGroupFormValues }> {
  const response = await unwrap<{ group: UserGroupResponse }>(await apiClient.PATCH("/v1/admin/users/groups/{groupId}" as never, {
    params: { header: csrfHeader(), path: { groupId } },
    body: { name: data.name, description: data.description, enabled: data.enabled },
  } as never));
  return { group: withGroupDefaults(response.group) };
}
