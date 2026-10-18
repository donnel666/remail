import type { DaumResourceItem } from "@/lib/daum-api";
import type { LifetimeType, ResourceStatus, UsageScope } from "./model";

export type { LifetimeType, ResourceStatus, UsageScope } from "./model";
export { getSuffix } from "./model";

export interface DaumEmailResource {
  id: number;
  version: number;
  emailAddress: string;
  forSale: boolean;
  usageScope: UsageScope;
  lifetimeType: LifetimeType;
  status: ResourceStatus;
  lastSafeError?: string;
  createdAt: string;
}

export const DAUM_CONSOLE_EMAIL_FORMAT_HINT = `email@daum.net----APP password
email@daum.net----login password----APP password
email@daum.net----login password----APP password----remail pickup URL----Kakao account email`;

export function toDaumEmailResource(item: DaumResourceItem): DaumEmailResource {
  return {
    id: item.id,
    version: item.version,
    emailAddress: item.email,
    forSale: item.forSale,
    usageScope: item.forSale ? "public_sale" : "private",
    lifetimeType: item.longLived ? "long_lived" : "short_lived",
    status: item.status,
    lastSafeError: item.lastSafeError,
    createdAt: item.createdAt,
  };
}
