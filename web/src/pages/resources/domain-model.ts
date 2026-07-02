import type { ResourceItem } from "@/lib/resources-api";

export type UsageScope = "private" | "public_sale";
export type DomainStatus = "normal" | "abnormal" | "disabled";

export interface DomainResource {
  id: number;
  domain: string;
  domainTld: string;
  mailServerId: number;
  usageScope: UsageScope;
  status: DomainStatus;
  mailboxCount: number;
  createdAt: string;
}

export function isDomainAvailable(status: DomainStatus) {
  return status === "normal";
}

export function isDomainDisabled(status: DomainStatus) {
  return status === "disabled";
}

export function getTldCounts(items: DomainResource[]) {
  const counts = new Map<string, number>();
  for (const item of items) {
    counts.set(item.domainTld, (counts.get(item.domainTld) ?? 0) + 1);
  }
  return Array.from(counts.entries()).sort(([left], [right]) =>
    left.localeCompare(right)
  );
}

function mapDomainStatus(status?: string): DomainStatus {
  switch (status) {
    case "normal":
      return "normal";
    case "disabled":
      return "disabled";
    default:
      return "abnormal";
  }
}

export function toDomainResource(item: ResourceItem): DomainResource | null {
  if (item.type !== "domain") return null;
  if (!item.domain) return null;
  if (!item.domainTld) return null;
  if (item.purpose === "binding") return null;

  return {
    id: item.id,
    domain: item.domain,
    domainTld: item.domainTld,
    mailServerId: item.mailServerId ?? 0,
    usageScope: item.purpose === "sale" ? "public_sale" : "private",
    status: mapDomainStatus(item.status),
    mailboxCount: item.mailboxCount ?? 0,
    createdAt: item.createdAt,
  };
}
