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
  apiRpmLimit: number;
  apiConcurrencyLimit: number;
  apiQuotaLimit: number;
  priceDiscountRatio: number;
  topupThreshold: number;
  autoUpgradeEnabled: boolean;
}

type UserGroupResponse = Omit<
  UserGroupFormValues,
  "apiRpmLimit" | "apiConcurrencyLimit" | "apiQuotaLimit" | "priceDiscountRatio" | "topupThreshold" | "autoUpgradeEnabled"
> & Partial<Pick<
  UserGroupFormValues,
  "apiRpmLimit" | "apiConcurrencyLimit" | "apiQuotaLimit" | "autoUpgradeEnabled"
>> & {
  priceDiscountRatio?: string;
  topupThreshold?: string;
};

function withGroupDefaults(group: UserGroupResponse): UserGroupFormValues {
  return {
    ...group,
    apiRpmLimit: group.apiRpmLimit ?? 0,
    apiConcurrencyLimit: group.apiConcurrencyLimit ?? 0,
    apiQuotaLimit: group.apiQuotaLimit ?? 0,
    priceDiscountRatio: Number(group.priceDiscountRatio ?? 1),
    topupThreshold: Number(group.topupThreshold ?? 0),
    autoUpgradeEnabled: group.autoUpgradeEnabled ?? false,
  };
}

export async function getUserGroups(): Promise<{ groups: UserGroupFormValues[] }> {
  const response = await unwrap<{ groups: UserGroupResponse[] }>(await apiClient.GET("/v1/admin/users/groups"));
  return { groups: response.groups.map(withGroupDefaults) };
}

export async function createUserGroup(data: Omit<UserGroupFormValues, "id">): Promise<{ group: UserGroupFormValues }> {
  const response = await unwrap<{ group: UserGroupResponse }>(await apiClient.POST("/v1/admin/users/groups", {
    params: { header: csrfHeader() },
    body: {
      ...data,
      priceDiscountRatio: String(data.priceDiscountRatio),
      topupThreshold: String(data.topupThreshold),
    },
  }));
  return { group: withGroupDefaults(response.group) };
}

export async function updateUserGroup(groupId: number, data: Partial<UserGroupFormValues>): Promise<{ group: UserGroupFormValues }> {
  const response = await unwrap<{ group: UserGroupResponse }>(await apiClient.PATCH("/v1/admin/users/groups/{groupId}", {
    params: { header: csrfHeader(), path: { groupId } },
    body: {
      name: data.name,
      description: data.description,
      enabled: data.enabled,
      apiRpmLimit: data.apiRpmLimit,
      apiConcurrencyLimit: data.apiConcurrencyLimit,
      apiQuotaLimit: data.apiQuotaLimit,
      priceDiscountRatio: data.priceDiscountRatio === undefined ? undefined : String(data.priceDiscountRatio),
      topupThreshold: data.topupThreshold === undefined ? undefined : String(data.topupThreshold),
      autoUpgradeEnabled: data.autoUpgradeEnabled,
    },
  }));
  return { group: withGroupDefaults(response.group) };
}
