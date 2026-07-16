import { describe, expect, it } from "vitest";

import type { ResourceItem } from "@/lib/resources-api";

import { toDomainResource } from "./domain-model";

describe("toDomainResource", () => {
  it("keeps binding domain resources returned by the API", () => {
    const resource: ResourceItem = {
      id: 42,
      type: "domain",
      ownerId: 7,
      domain: "auxiliary.example.com",
      domainTld: ".com",
      mailServerId: 3,
      purpose: "binding",
      status: "normal",
      mailboxCount: 2,
      createdAt: "2026-07-15T00:00:00Z",
      updatedAt: "2026-07-15T00:00:00Z",
    };

    expect(toDomainResource(resource)).toEqual({
      id: 42,
      domain: "auxiliary.example.com",
      domainTld: ".com",
      mailServerId: 3,
      usageScope: "private",
      status: "normal",
      lastSafeError: undefined,
      mailboxCount: 2,
      createdAt: "2026-07-15T00:00:00Z",
    });
  });

  it.each(["pending", "validating"] as const)(
    "preserves the %s validation lifecycle state",
    (status) => {
      const resource: ResourceItem = {
        id: 43,
        type: "domain",
        ownerId: 7,
        domain: "validation.example.com",
        domainTld: ".com",
        mailServerId: 3,
        purpose: "not_sale",
        status,
        createdAt: "2026-07-15T00:00:00Z",
        updatedAt: "2026-07-15T00:00:00Z",
      };

      expect(toDomainResource(resource)?.status).toBe(status);
    }
  );
});
