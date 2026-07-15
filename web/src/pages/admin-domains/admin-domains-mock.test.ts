import { describe, expect, it } from "vitest";

import {
  deleteAdminDomain,
  deleteAdminDomainsByFilter,
  deleteAdminDomainsByIds,
  getAdminDomainDetail,
  listAdminDomains,
  queueAdminDomainMailFetch,
} from "./admin-domains-mock";

describe("admin domain mock commands", () => {
  it("persists a queued mail-fetch task in the next detail response", async () => {
    const list = await listAdminDomains({}, 0, 1);
    const domain = list.items[0];
    const before = await getAdminDomainDetail(domain.id);

    const queued = await queueAdminDomainMailFetch(domain.id);
    const after = await getAdminDomainDetail(domain.id);

    expect(queued).toMatchObject({ kind: "mail_fetch", status: "queued" });
    expect(after.tasks).toHaveLength(before.tasks.length + 1);
    expect(after.tasks[0]).toEqual(queued);
  });

  it("applies the same deleted-state transition to every delete command", async () => {
    const list = await listAdminDomains({ purpose: "sale" }, 0, 3);
    expect(list.items).toHaveLength(3);
    const [single, selected, filtered] = list.items;

    await deleteAdminDomain(single.id);
    await deleteAdminDomainsByIds([selected.id]);
    await deleteAdminDomainsByFilter({ search: filtered.domain });

    const deleted = await Promise.all(
      [single.id, selected.id, filtered.id].map(getAdminDomainDetail)
    );
    for (const detail of deleted) {
      expect(detail.status).toBe("deleted");
      expect(detail.purpose).toBe("not_sale");
      expect(detail.mailboxCount).toBe(0);
      expect(detail.lastAllocatedAt).toBeUndefined();
    }
  });
});
