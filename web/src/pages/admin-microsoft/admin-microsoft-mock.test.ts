import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type AdminMicrosoftMock = typeof import("./admin-microsoft-mock");

// The auxiliary mailbox (bindingAddress) is intentionally surfaced and editable
// per an explicit administrator decision, so it is not treated as a forbidden
// response key here. Raw account secrets must still never appear.
const SENSITIVE_RESPONSE_KEYS = [
  "password",
  "refreshToken",
  "accessToken",
  "clientId",
  "claimToken",
  "dispatchToken",
] as const;

let api: AdminMicrosoftMock;

async function complete<T>(operation: Promise<T>): Promise<T> {
  await vi.runAllTimersAsync();
  return operation;
}

function collectKeys(value: unknown, keys = new Set<string>()): Set<string> {
  if (Array.isArray(value)) {
    for (const item of value) collectKeys(item, keys);
    return keys;
  }
  if (!value || typeof value !== "object") return keys;

  for (const [key, nested] of Object.entries(
    value as Record<string, unknown>
  )) {
    keys.add(key);
    collectKeys(nested, keys);
  }
  return keys;
}

function expectSafeResponseDto(value: unknown) {
  const keys = collectKeys(value);
  for (const key of SENSITIVE_RESPONSE_KEYS) {
    expect(keys.has(key), `response contains sensitive key ${key}`).toBe(false);
  }
}

function activeValidations(
  detail: Awaited<
    ReturnType<AdminMicrosoftMock["getAdminMicrosoftResourceDetail"]>
  >
) {
  return detail.asyncTasks.validations.filter(
    (task) => task.status === "queued" || task.status === "running"
  );
}

describe("admin Microsoft mock API", () => {
  beforeEach(async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-11T08:00:00.000Z"));
    vi.resetModules();
    api = await import("./admin-microsoft-mock");
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it("filters by suffix, owner and token health while keeping cross-filter facets", async () => {
    const baseline = await complete(
      api.listAdminMicrosoftResources({}, 0, 50)
    );
    const seed = baseline.items[0];

    expect(seed).toBeDefined();
    expect(baseline.items.every((item) => item.status !== "deleted")).toBe(
      true
    );
    expect(baseline.facets.status.deleted).toBeGreaterThan(0);

    const suffixResult = await complete(
      api.listAdminMicrosoftResources(
        { suffix: seed.suffix.slice(1) },
        0,
        200
      )
    );
    expect(suffixResult.total).toBeGreaterThan(0);
    expect(
      suffixResult.items.every((item) => item.suffix === seed.suffix)
    ).toBe(true);
    expect(
      suffixResult.facets.suffixes.find(
        (facet) => facet.key === seed.suffix
      )?.count
    ).toBe(suffixResult.total);

    const ownerResult = await complete(
      api.listAdminMicrosoftResources({ ownerId: seed.ownerId }, 0, 200)
    );
    expect(ownerResult.total).toBeGreaterThan(0);
    expect(
      ownerResult.items.every((item) => item.ownerId === seed.ownerId)
    ).toBe(true);
    expect(
      ownerResult.facets.owners.find(
        (facet) => facet.key === seed.ownerId
      )?.count
    ).toBe(ownerResult.total);

    const tokenResult = await complete(
      api.listAdminMicrosoftResources(
        { tokenHealth: seed.tokenHealth },
        0,
        200
      )
    );
    expect(tokenResult.total).toBeGreaterThan(0);
    expect(
      tokenResult.items.every(
        (item) => item.tokenHealth === seed.tokenHealth
      )
    ).toBe(true);
    expect(tokenResult.facets.tokenHealth[seed.tokenHealth]).toBe(
      tokenResult.total
    );

    const combined = await complete(
      api.listAdminMicrosoftResources(
        {
          ownerId: seed.ownerId,
          suffix: seed.suffix,
          tokenHealth: seed.tokenHealth,
        },
        0,
        200
      )
    );
    expect(combined.total).toBeGreaterThan(0);
    expect(
      combined.items.every(
        (item) =>
          item.ownerId === seed.ownerId &&
          item.suffix === seed.suffix &&
          item.tokenHealth === seed.tokenHealth
      )
    ).toBe(true);
    expect(
      combined.facets.suffixes.find(
        (facet) => facet.key === seed.suffix
      )?.count
    ).toBeGreaterThanOrEqual(combined.total);
    expect(
      combined.facets.owners.find(
        (facet) => facet.key === seed.ownerId
      )?.count
    ).toBeGreaterThanOrEqual(combined.total);
    expect(combined.facets.tokenHealth[seed.tokenHealth]).toBeGreaterThanOrEqual(
      combined.total
    );
  });

  it("returns the same page for offset and afterId pagination", async () => {
    const firstPage = await complete(
      api.listAdminMicrosoftResources({}, 0, 5)
    );
    const offsetPage = await complete(
      api.listAdminMicrosoftResources({}, 5, 5)
    );

    expect(firstPage.nextAfterId).toBeDefined();
    const cursorPage = await complete(
      api.listAdminMicrosoftResources({}, 0, 5, firstPage.nextAfterId)
    );

    expect(offsetPage.offset).toBe(5);
    expect(cursorPage.offset).toBe(5);
    expect(cursorPage.items.map((item) => item.id)).toEqual(
      offsetPage.items.map((item) => item.id)
    );
    expect(
      cursorPage.items.some((item) =>
        firstPage.items.some((first) => first.id === item.id)
      )
    ).toBe(false);
  });

  it("keeps owner group snapshots stable and refreshes them when ownership changes", async () => {
    const owners = await complete(api.listAdminMicrosoftOwners());
    const page = await complete(
      api.listAdminMicrosoftResources({ forSale: false }, 0, 50)
    );
    const target = page.items[0];
    const currentOwner = owners.find((owner) => owner.id === target.ownerId);
    const replacementOwner = owners.find(
      (owner) => owner.enabled && owner.id !== target.ownerId
    );

    expect(owners.length).toBeGreaterThan(1);
    expect(
      owners.every((owner) => owner.groupName.trim().length > 0)
    ).toBe(true);
    expect(target.ownerGroupName).toBe(currentOwner?.groupName);
    expect(
      page.facets.owners.find((owner) => owner.key === target.ownerId)?.groupName
    ).toBe(currentOwner?.groupName);

    const before = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );
    expect(before.ownerGroupName).toBe(currentOwner?.groupName);

    expect(replacementOwner).toBeDefined();
    if (!replacementOwner) throw new Error("Replacement owner is required.");

    const updated = await complete(
      api.updateAdminMicrosoftResource(target.id, {
        ownerId: replacementOwner.id,
      })
    );
    const after = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );

    expect(updated).toMatchObject({
      ownerId: replacementOwner.id,
      ownerEmail: replacementOwner.email,
      ownerNickname: replacementOwner.nickname,
      ownerGroupName: replacementOwner.groupName,
      ownerRole: replacementOwner.role,
    });
    expect(after).toMatchObject({
      ownerId: replacementOwner.id,
      ownerEmail: replacementOwner.email,
      ownerNickname: replacementOwner.nickname,
      ownerGroupName: replacementOwner.groupName,
      ownerRole: replacementOwner.role,
    });

    const refreshedPage = await complete(
      api.listAdminMicrosoftResources({ search: String(target.id) }, 0, 50)
    );
    expect(
      refreshedPage.items.find((item) => item.id === target.id)
    ).toMatchObject({
      ownerGroupName: replacementOwner.groupName,
      ownerRole: replacementOwner.role,
    });
  });

  it("supports the 1000-row block size used by the paged-list hook", async () => {
    const block = await complete(
      api.listAdminMicrosoftResources({}, 0, 1_000)
    );

    expect(block.total).toBeGreaterThan(200);
    expect(block.items).toHaveLength(block.total);
  });

  it("deep-clones list and detail DTOs before returning them", async () => {
    const page = await complete(api.listAdminMicrosoftResources({}, 0, 1));
    const returnedItem = page.items[0];
    const originalExplicitCount = returnedItem.aliasCounts.explicit;
    returnedItem.aliasCounts.explicit = 99_999;

    const firstDetail = await complete(
      api.getAdminMicrosoftResourceDetail(returnedItem.id)
    );
    const original = {
      credentialRevision: firstDetail.credentials.revision,
      scopes: [...firstDetail.token.scopes],
      weekCreated: firstDetail.aliasSchedule.weekCreated,
      dotAddress: firstDetail.aliasSamples.dot[0]?.emailAddress,
      validationError: firstDetail.asyncTasks.validations[0]?.safeError,
    };

    firstDetail.credentials.revision = 99_999;
    firstDetail.token.scopes.push("mutated.scope");
    firstDetail.aliasSchedule.weekCreated = 99_999;
    if (firstDetail.aliasSamples.dot[0]) {
      firstDetail.aliasSamples.dot[0].emailAddress = "mutated@example.com";
    }
    if (firstDetail.asyncTasks.validations[0]) {
      firstDetail.asyncTasks.validations[0].safeError = "mutated error";
    }

    const secondDetail = await complete(
      api.getAdminMicrosoftResourceDetail(returnedItem.id)
    );
    const secondPage = await complete(
      api.listAdminMicrosoftResources({ search: String(returnedItem.id) }, 0, 20)
    );
    const storedItem = secondPage.items.find(
      (item) => item.id === returnedItem.id
    );

    expect(storedItem?.aliasCounts.explicit).toBe(originalExplicitCount);
    expect(secondDetail.credentials.revision).toBe(original.credentialRevision);
    expect(secondDetail.token.scopes).toEqual(original.scopes);
    expect(secondDetail.aliasSchedule.weekCreated).toBe(original.weekCreated);
    expect(secondDetail.aliasSamples.dot[0]?.emailAddress).toBe(
      original.dotAddress
    );
    expect(secondDetail.asyncTasks.validations[0]?.safeError).toBe(
      original.validationError
    );
  });

  it("reuses the same active validation job on repeated submissions", async () => {
    const normalResources = await complete(
      api.listAdminMicrosoftResources({ status: "normal" }, 0, 20)
    );
    const target = normalResources.items[0];
    const before = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );

    expect(activeValidations(before)).toHaveLength(0);

    const first = await complete(
      api.validateAdminMicrosoftResource(target.id)
    );
    const second = await complete(
      api.validateAdminMicrosoftResource(target.id)
    );
    const after = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );
    const active = activeValidations(after);

    expect(first.validationIds).toHaveLength(1);
    expect(second.validationIds).toEqual(first.validationIds);
    expect(second.requestId).toBe(first.requestId);
    expect(active).toHaveLength(1);
    expect(active[0].id).toBe(first.validationIds?.[0]);
    expect(after.asyncTasks.validations).toHaveLength(
      before.asyncTasks.validations.length + 1
    );
    expect(after.status).toBe("pending");
  });

  it("marks a resource pending after credential replacement without echoing secrets", async () => {
    const normalResources = await complete(
      api.listAdminMicrosoftResources({ status: "normal" }, 0, 20)
    );
    const target = normalResources.items[0];
    const before = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );
    const secrets = {
      password: "PASSWORD_DO_NOT_ECHO_7f7b",
      clientId: "CLIENT_ID_DO_NOT_ECHO_91c2",
      refreshToken: "REFRESH_TOKEN_DO_NOT_ECHO_c218",
    };

    const replaced = await complete(
      api.replaceAdminMicrosoftCredentials(target.id, secrets)
    );

    expect(replaced.status).toBe("pending");
    expect(replaced.graphAvailable).toBe(false);
    expect(replaced.credentials).toMatchObject({
      passwordConfigured: true,
      clientIdConfigured: true,
      refreshTokenConfigured: true,
      revision: before.credentials.revision + 1,
    });
    expect(activeValidations(replaced)).toHaveLength(1);

    const serialized = JSON.stringify(replaced);
    expect(serialized).not.toContain(secrets.password);
    expect(serialized).not.toContain(secrets.clientId);
    expect(serialized).not.toContain(secrets.refreshToken);
    expectSafeResponseDto(replaced);

    const refreshed = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );
    expect(JSON.stringify(refreshed)).not.toContain(secrets.password);
    expect(JSON.stringify(refreshed)).not.toContain(secrets.clientId);
    expect(JSON.stringify(refreshed)).not.toContain(secrets.refreshToken);
    expectSafeResponseDto(refreshed);
  });

  it("keeps RT expiry and health independent when long-lived classification changes", async () => {
    const resources = await complete(
      api.listAdminMicrosoftResources({ tokenHealth: "valid" }, 0, 20)
    );
    const target = resources.items[0];
    const before = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );

    const updated = await complete(
      api.updateAdminMicrosoftResource(target.id, {
        longLived: !before.longLived,
      })
    );
    const after = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );

    expect(updated.longLived).toBe(!before.longLived);
    expect(updated.rtExpireAt).toBe(before.rtExpireAt);
    expect(updated.tokenHealth).toBe(before.tokenHealth);
    expect(after.token.rtExpireAt).toBe(before.token.rtExpireAt);
    expect(after.token.health).toBe(before.token.health);
  });

  it("allows an administrator to delete a public-sale resource", async () => {
    const publicResources = await complete(
      api.listAdminMicrosoftResources({ forSale: true }, 0, 20)
    );
    const target = publicResources.items[0];

    expect(target).toBeDefined();
    expect(target.forSale).toBe(true);
    expect(target.status).not.toBe("deleted");

    await complete(api.deleteAdminMicrosoftResource(target.id));

    const deletedView = await complete(
      api.listAdminMicrosoftResources(
        { search: String(target.id), status: "deleted" },
        0,
        200
      )
    );
    const deleted = deletedView.items.find((item) => item.id === target.id);

    expect(deleted).toBeDefined();
    expect(deleted?.status).toBe("deleted");
    expect(deleted?.forSale).toBe(false);
  });

  it("bulk-deletes public and private resources in one administrator command", async () => {
    const publicResources = await complete(
      api.listAdminMicrosoftResources({ forSale: true }, 0, 20)
    );
    const privateResources = await complete(
      api.listAdminMicrosoftResources({ forSale: false }, 0, 20)
    );
    const publicTarget = publicResources.items[0];
    const privateTarget = privateResources.items[0];
    const targetIds = [publicTarget.id, privateTarget.id];

    expect(publicTarget.forSale).toBe(true);
    expect(privateTarget.forSale).toBe(false);

    const result = await complete(
      api.deleteAdminMicrosoftResourcesByIds(targetIds)
    );

    expect(result.affected).toBe(2);
    expect(result.resourceIds).toEqual(expect.arrayContaining(targetIds));

    for (const id of targetIds) {
      const deletedView = await complete(
        api.listAdminMicrosoftResources(
          { search: String(id), status: "deleted" },
          0,
          200
        )
      );
      const deleted = deletedView.items.find((item) => item.id === id);
      expect(deleted).toMatchObject({ id, status: "deleted", forSale: false });
    }
  });

  it("never serializes raw credential, token, task-token, or binding fields", async () => {
    const page = await complete(api.listAdminMicrosoftResources({}, 0, 25));
    const detail = await complete(
      api.getAdminMicrosoftResourceDetail(page.items[0].id)
    );
    const owners = await complete(api.listAdminMicrosoftOwners());

    expectSafeResponseDto(page);
    expectSafeResponseDto(detail);
    expectSafeResponseDto(owners);

    const serialized = JSON.stringify({ page, detail, owners });
    for (const key of SENSITIVE_RESPONSE_KEYS) {
      expect(serialized).not.toMatch(new RegExp(`"${key}"\\s*:`));
    }
  });

  it("returns consistent mail and allocation read models on the resource detail", async () => {
    const normal = await complete(
      api.listAdminMicrosoftResources({ status: "normal" }, 0, 20)
    );
    const target = normal.items[0];
    const detail = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );

    expect(Array.isArray(detail.messages)).toBe(true);
    expect(Array.isArray(detail.allocations)).toBe(true);
    expect(detail.allocations.length).toBeGreaterThan(0);

    const mailboxKinds = new Set(["main", "alias", "dot", "plus"]);
    const messageStatuses = new Set(["received", "matched", "ignored"]);
    for (const message of detail.messages) {
      expect(mailboxKinds.has(message.mailbox)).toBe(true);
      expect(messageStatuses.has(message.status)).toBe(true);
      if (message.status === "matched") {
        expect(message.verificationCode).toBeTruthy();
        expect(message.orderNo).toBeTruthy();
      } else {
        expect(message.verificationCode).toBeUndefined();
      }
    }

    const allocationStatuses = new Set(["allocated", "released"]);
    const supplyScopes = new Set(["owned", "public"]);
    for (const allocation of detail.allocations) {
      expect(mailboxKinds.has(allocation.mailbox)).toBe(true);
      expect(allocationStatuses.has(allocation.status)).toBe(true);
      expect(supplyScopes.has(allocation.supplyScope)).toBe(true);
      const matchedForAddress = detail.messages.filter(
        (message) =>
          message.status === "matched" &&
          message.recipient === allocation.deliveryEmail
      ).length;
      expect(allocation.mailCount).toBe(matchedForAddress);
    }

    // An allocated alias reflects the order/project it serves.
    const allocatedAliasEmails = new Set(
      detail.allocations
        .filter(
          (allocation) =>
            allocation.status === "allocated" && allocation.mailbox !== "main"
        )
        .map((allocation) => allocation.deliveryEmail)
    );
    const aliasSamples = [
      ...detail.aliasSamples.explicit,
      ...detail.aliasSamples.dot,
      ...detail.aliasSamples.plus,
    ];
    for (const sample of aliasSamples) {
      if (allocatedAliasEmails.has(sample.emailAddress)) {
        expect(sample.status).toBe("allocated");
        expect(sample.orderNo).toBeTruthy();
      }
    }

    // Usage summary aggregates the same facts.
    expect(detail.usageSummary.totalOrders).toBe(detail.allocations.length);
    expect(detail.usageSummary.totalMails).toBe(detail.messages.length);
    expect(detail.usageSummary.activeAllocations).toBe(
      detail.allocations.filter(
        (allocation) => allocation.status === "allocated"
      ).length
    );
    const projectOrderTotal = detail.usageSummary.projects.reduce(
      (sum, project) => sum + project.orderCount,
      0
    );
    expect(projectOrderTotal).toBe(detail.allocations.length);

    expectSafeResponseDto(detail);
  });

  it("returns empty mail and allocation read models for pending resources", async () => {
    const pending = await complete(
      api.listAdminMicrosoftResources({ status: "pending" }, 0, 20)
    );
    const target = pending.items[0];

    expect(target).toBeDefined();
    const detail = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );

    expect(detail.messages).toHaveLength(0);
    expect(detail.allocations).toHaveLength(0);
    expect(detail.usageSummary.totalOrders).toBe(0);
    expect(detail.usageSummary.totalMails).toBe(0);
    expect(detail.usageSummary.projects).toHaveLength(0);
  });

  it("publishes and unpublishes resources in bulk while skipping ineligible owners", async () => {
    const privateList = await complete(
      api.listAdminMicrosoftResources({ forSale: false }, 0, 200)
    );
    const owners = await complete(api.listAdminMicrosoftOwners());
    const eligibleOwnerIds = new Set(
      owners
        .filter((owner) => owner.enabled && owner.role !== "user")
        .map((owner) => owner.id)
    );
    const eligible = privateList.items.find(
      (item) => eligibleOwnerIds.has(item.ownerId) && item.status !== "deleted"
    );

    expect(eligible).toBeDefined();
    if (!eligible) throw new Error("An eligible private resource is required.");

    const published = await complete(
      api.setAdminMicrosoftResourcesForSaleByIds([eligible.id], true)
    );
    expect(published.resourceIds).toContain(eligible.id);
    const afterPublish = await complete(
      api.listAdminMicrosoftResources({ search: String(eligible.id) }, 0, 20)
    );
    expect(afterPublish.items.find((item) => item.id === eligible.id)?.forSale).toBe(
      true
    );

    // Publishing an already public resource is an idempotent no-op.
    const again = await complete(
      api.setAdminMicrosoftResourcesForSaleByIds([eligible.id], true)
    );
    expect(again.affected).toBe(0);

    const unpublished = await complete(
      api.setAdminMicrosoftResourcesForSaleByIds([eligible.id], false)
    );
    expect(unpublished.resourceIds).toContain(eligible.id);
    const afterPrivate = await complete(
      api.listAdminMicrosoftResources({ search: String(eligible.id) }, 0, 20)
    );
    expect(
      afterPrivate.items.find((item) => item.id === eligible.id)?.forSale
    ).toBe(false);

    // A resource owned by an ineligible (plain user / disabled) owner is skipped.
    const ineligible = privateList.items.find(
      (item) => !eligibleOwnerIds.has(item.ownerId) && item.status !== "deleted"
    );
    if (ineligible) {
      const result = await complete(
        api.setAdminMicrosoftResourcesForSaleByIds([ineligible.id], true)
      );
      expect(result.affected).toBe(0);
    }
  });

  it("edits the resource email, re-derives the suffix and rejects duplicates", async () => {
    const page = await complete(
      api.listAdminMicrosoftResources({ status: "normal" }, 0, 20)
    );
    const target = page.items[0];
    const other = page.items[1];

    const updated = await complete(
      api.updateAdminMicrosoftResource(target.id, {
        emailAddress: "Edited.Fallback@Hotmail.CO.UK",
      })
    );
    expect(updated.emailAddress).toBe("edited.fallback@hotmail.co.uk");
    expect(updated.suffix).toBe("@hotmail.co.uk");

    // An invalid format is rejected. The rejection handler is attached
    // synchronously so the timer flush does not surface a floating rejection.
    const invalid = api
      .updateAdminMicrosoftResource(target.id, { emailAddress: "not-an-email" })
      .then(
        () => "resolved" as const,
        () => "rejected" as const
      );
    await vi.runAllTimersAsync();
    expect(await invalid).toBe("rejected");

    // A duplicate of another live resource's email is rejected.
    const duplicate = api
      .updateAdminMicrosoftResource(other.id, {
        emailAddress: updated.emailAddress,
      })
      .then(
        () => "resolved" as const,
        () => "rejected" as const
      );
    await vi.runAllTimersAsync();
    expect(await duplicate).toBe("rejected");
  });

  it("surfaces and edits the auxiliary mailbox in the list and detail", async () => {
    const page = await complete(api.listAdminMicrosoftResources({}, 0, 200));
    // The seed contains resources with an auxiliary mailbox surfaced on the item.
    expect(page.items.some((item) => Boolean(item.bindingAddress))).toBe(true);

    const target = page.items[0];
    const withBinding = await complete(
      api.updateAdminMicrosoftResource(target.id, {
        bindingAddress: "Recovery.Fallback@Gmail.com",
      })
    );
    expect(withBinding.bindingAddress).toBe("recovery.fallback@gmail.com");

    const detail = await complete(
      api.getAdminMicrosoftResourceDetail(target.id)
    );
    expect(detail.bindingAddress).toBe("recovery.fallback@gmail.com");

    const cleared = await complete(
      api.updateAdminMicrosoftResource(target.id, { bindingAddress: "" })
    );
    expect(cleared.bindingAddress).toBeUndefined();
  });
});
