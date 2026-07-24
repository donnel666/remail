// @ts-expect-error -- Vitest executes this source-contract test in Node; the
// browser application intentionally does not depend on Node type packages.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import {
  ALLOCATION_KEYS,
  EMAIL_RESOURCE_KEYS,
  EMAIL_SERVICE_KEYS,
  MAIL_DELIVERY_KEYS,
  MAILMATCH_KEYS,
  MICROSOFT_OPS_KEYS,
  PROXY_NETWORK_KEYS,
} from "./email-service-keys";

const defaultsSource = readFileSync(
  new URL("../../../../internal/systemsettings/runtimeconfig/defaults.go", import.meta.url),
  "utf8",
);
const backendKeys = [...defaultsSource.matchAll(/\{Key: "([^\"]+)"/g)].map((match) => match[1]);
const frontendGroups = [
  EMAIL_RESOURCE_KEYS,
  ALLOCATION_KEYS,
  MAILMATCH_KEYS,
  MICROSOFT_OPS_KEYS,
  PROXY_NETWORK_KEYS,
  MAIL_DELIVERY_KEYS,
];

describe("email service setting keys", () => {
  it("keeps frontend groups unique and aligned with backend defaults", () => {
    const frontendKeys = frontendGroups.flat();
    expect(new Set(frontendKeys).size).toBe(frontendKeys.length);
    expect(new Set(frontendKeys)).toEqual(new Set(backendKeys));
    expect(EMAIL_SERVICE_KEYS).toEqual(frontendKeys);
  });
});
