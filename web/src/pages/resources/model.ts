export type UsageScope = "private" | "public_sale";
export type LifetimeType = "short_lived" | "long_lived";
export type ResourceStatus =
  | "pending_validation"
  | "active"
  | "available"
  | "disabled"
  | "expired"
  | "isolated"
  | "invalid";

export interface EmailResource {
  id: number;
  emailAddress: string;
  emailType: string;
  usageScope: UsageScope;
  lifetimeType: LifetimeType;
  status: ResourceStatus;
  validationFailureReason?: string;
}

const MICROSOFT_SUFFIXES = [
  "@outlook.com",
  "@hotmail.com",
  "@live.com",
  "@msn.com",
  "@outlook.jp",
  "@hotmail.co.uk",
  "@outlook.de",
  "@outlook.fr",
];

const MICROSOFT_STATUSES: ResourceStatus[] = [
  "available",
  "available",
  "available",
  "pending_validation",
  "disabled",
  "expired",
  "isolated",
];

const MICROSOFT_FAILURE_REASONS: Partial<Record<ResourceStatus, string>> = {
  disabled: "Account disabled by system policy",
  expired: "Refresh token expired, re-authenticate required",
  isolated: "Mailbox isolated after abnormal sign-in",
  invalid: "Mailbox credentials failed validation",
};

function getEmailType(suffix: string) {
  return suffix
    .replace("@", "")
    .replace(/\./g, "_")
    .replace(/-/g, "_");
}

function createMockMicrosoftEmail(index: number): EmailResource {
  const suffix = MICROSOFT_SUFFIXES[index % MICROSOFT_SUFFIXES.length];
  const status = MICROSOFT_STATUSES[index % MICROSOFT_STATUSES.length];
  const id = index + 1;

  return {
    id,
    emailAddress: `resource_${String(id).padStart(3, "0")}${suffix}`,
    emailType: getEmailType(suffix),
    usageScope: index % 4 === 0 ? "public_sale" : "private",
    lifetimeType: index % 6 === 0 ? "short_lived" : "long_lived",
    status,
    validationFailureReason: MICROSOFT_FAILURE_REASONS[status],
  };
}

export const MICROSOFT_EMAIL_RESOURCES_MOCK: EmailResource[] = Array.from(
  { length: 100 },
  (_, index) => createMockMicrosoftEmail(index)
);

export const MICROSOFT_EMAIL_FORMAT_HINT = `email----password
email----password----binding_email
email----password----clientID----refreshToken
email----password----clientID----refreshToken----binding_email`;

export function getSuffix(email: string): string {
  const index = email.lastIndexOf("@");
  return index === -1 ? "" : email.slice(index).toLowerCase();
}

export function isAvailable(status: ResourceStatus) {
  return status === "available" || status === "active";
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
