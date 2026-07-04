import type { ResourceItem } from "@/lib/resources-api";

export type UsageScope = "private" | "public_sale";
export type LifetimeType = "short_lived" | "long_lived";
export type ResourceStatus = "pending" | "normal" | "abnormal" | "disabled";

export interface EmailResource {
  id: number;
  emailAddress: string;
  emailType: string;
  forSale: boolean;
  usageScope: UsageScope;
  lifetimeType: LifetimeType;
  graphAvailable: boolean;
  status: ResourceStatus;
  lastSafeError?: string;
  createdAt: string;
}

function getEmailType(suffix: string) {
  return suffix
    .replace("@", "")
    .replace(/\./g, "_")
    .replace(/-/g, "_");
}

export const MICROSOFT_EMAIL_FORMAT_HINT = `email----password
email----password----bindingAddress
email----password----clientID----refreshToken
email----password----clientID----refreshToken----bindingAddress`;

export function getSuffix(email: string): string {
  const index = email.lastIndexOf("@");
  return index === -1 ? "" : email.slice(index).toLowerCase();
}

export function isNormal(status: ResourceStatus) {
  return status === "normal";
}

export function toResourceStatus(status?: string): ResourceStatus {
  switch (status) {
    case "pending":
    case "normal":
    case "abnormal":
    case "disabled":
      return status;
    default:
      return "pending";
  }
}

export function toEmailResource(item: ResourceItem): EmailResource | null {
  if (item.type !== "microsoft" || !item.email) return null;

  const forSale = Boolean(item.forSale);
  const suffix = getSuffix(item.email);
  return {
    id: item.id,
    emailAddress: item.email,
    emailType: getEmailType(suffix),
    forSale,
    usageScope: forSale ? "public_sale" : "private",
    lifetimeType: item.longLived ? "long_lived" : "short_lived",
    graphAvailable: Boolean(item.graphAvailable),
    status: toResourceStatus(item.status),
    lastSafeError: item.lastSafeError || undefined,
    createdAt: item.createdAt,
  };
}

export function getSuffixCounts(items: EmailResource[]) {
  const counts = new Map<string, number>();
  for (const item of items) {
    const suffix = getSuffix(item.emailAddress);
    counts.set(suffix, (counts.get(suffix) ?? 0) + 1);
  }
  return Array.from(counts.entries()).sort(([left], [right]) =>
    left.localeCompare(right)
  );
}
