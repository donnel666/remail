// @ts-expect-error -- Vitest executes this source-contract test in Node; the
// browser application intentionally does not depend on Node type packages.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { AUTH_SECURITY_KEYS } from "./auth-security-keys";

import {
  ALLOCATION_KEYS,
  EMAIL_RESOURCE_KEYS,
  EMAIL_SERVICE_KEYS,
  MAIL_DELIVERY_KEYS,
  MAILMATCH_KEYS,
  MICROSOFT_OPS_KEYS,
  PROXY_NETWORK_KEYS,
} from "./email-service-keys";
import {
  ADMIN_MONITOR_KEYS,
  BACKGROUND_JOB_KEYS,
  BATCH_OPERATION_KEYS,
  RETENTION_KEYS,
} from "./system-operations-keys";
import { DAILY_CHECKIN_REWARD_KEYS, LEADERBOARD_REWARD_KEYS, RECHARGE_REBATE_KEYS } from "./users-rebates-keys";
import { PAYMENT_BILLING_KEYS } from "./payment-billing-keys";

const defaultsSource = readFileSync(
  new URL("../../../../internal/systemsettings/runtimeconfig/defaults.go", import.meta.url),
  "utf8",
);
const emailResourcesSource = readFileSync(new URL("./email-resources.tsx", import.meta.url), "utf8");
const projectPricingSource = readFileSync(new URL("./project-pricing.tsx", import.meta.url), "utf8");
const backendKeys = [...defaultsSource.matchAll(/\{Key: "([^\"]+)"/g)].map((match) => match[1]);
const nonUIRuntimeKeys = new Set([
  "admin_resource_list_default_limit",
  "admin_log_default_limit",
  "admin_task_default_limit",
  "admin_message_default_limit",
  "points_unit_migration_v1",
]);
const emailServiceGroups = [
  EMAIL_RESOURCE_KEYS,
  ALLOCATION_KEYS,
  MAILMATCH_KEYS,
  MICROSOFT_OPS_KEYS,
  PROXY_NETWORK_KEYS,
  MAIL_DELIVERY_KEYS,
];
const frontendGroups = [
  [
    "announcement_enabled",
    "announcements",
    "global_notice",
    "faq_enabled",
    "faq_list",
    "customer_service_qq_group_number",
    "customer_service_qq_group_url",
    "customer_service_telegram_group_url",
  ],
  AUTH_SECURITY_KEYS,
  PAYMENT_BILLING_KEYS,
  RECHARGE_REBATE_KEYS,
  DAILY_CHECKIN_REWARD_KEYS,
  LEADERBOARD_REWARD_KEYS,
  ...emailServiceGroups,
  BACKGROUND_JOB_KEYS,
  BATCH_OPERATION_KEYS,
  RETENTION_KEYS,
  ADMIN_MONITOR_KEYS,
];

describe("system setting keys", () => {
  it("keeps frontend groups unique and aligned with backend defaults", () => {
    const emailServiceKeys = emailServiceGroups.flat();
    const frontendKeys = frontendGroups.flat();
    const visibleBackendKeys = backendKeys.filter((key) => !nonUIRuntimeKeys.has(key));
    expect(new Set(frontendKeys).size).toBe(frontendKeys.length);
    expect(new Set(frontendKeys)).toEqual(new Set(visibleBackendKeys));
    expect(EMAIL_SERVICE_KEYS).toEqual(emailServiceKeys);
  });

  it("limits iCloud forwarding suffixes to active auxiliary domains", () => {
    expect(emailResourcesSource).toContain('listAdminDomains({ purpose: "binding" }');
    expect(emailResourcesSource).toContain('item.status !== "disabled"');
    expect(emailResourcesSource).toContain('item.status !== "deleted"');
    expect(emailResourcesSource).toContain('Toast.error(getIamErrorMessage');
    expect(emailResourcesSource).toContain('multiple');
    expect(emailResourcesSource).toContain('update("icloud_forwarding_suffixes"');
    expect(emailResourcesSource).toContain('update("icloud_cookie_keepalive_minutes"');
    expect(emailResourcesSource).toContain('update("icloud_onboarding_max_attempts"');
  });

  it("exposes Gmail variant prices and per-product service defaults", () => {
    expect(projectPricingSource).toContain("default_project_gmail_variant_code_price");
    expect(projectPricingSource).toContain("default_project_gmail_variant_purchase_supplier_price");
    expect(projectPricingSource).toContain("default_project_gmail_variant_code_enabled");
    expect(projectPricingSource).toContain("default_project_icloud_purchase_enabled");
    expect(projectPricingSource).toContain("每种商品至少默认启用接码或购买中的一项");
    expect(projectPricingSource).toContain("ariaLabel={`${t(productLabel)} · ${t(label)}`}");
  });
});
