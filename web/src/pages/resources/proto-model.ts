import type { ProtoResource, ProtoResourceStatus } from "@/lib/proto-api";

export type UsageScope = "private" | "public_sale";
export type LifetimeType = "short_lived" | "long_lived";
export type ResourceStatus = ProtoResourceStatus;
export interface EmailResource {
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
export const PROTO_DEFAULT_EMAIL_SUFFIX = "@proton.me";
export const PROTO_EMAIL_FORMAT_HINT = `email----password\nemail----password----base64(PKL)\naccount → account${PROTO_DEFAULT_EMAIL_SUFFIX}`;
export function getSuffix(email: string) {
  const index = email.lastIndexOf("@");
  return index < 0 ? "" : email.slice(index).toLowerCase();
}
export function toEmailResource(item: ProtoResource): EmailResource {
  return { id: item.id, version: item.version, emailAddress: item.email, forSale: item.forSale,
    usageScope: item.forSale ? "public_sale" : "private",
    lifetimeType: item.longLived ? "long_lived" : "short_lived",
    status: item.status, lastSafeError: item.lastSafeError, createdAt: item.createdAt };
}
