import { expect, it, vi } from "vitest";

const apiMocks = vi.hoisted(() => ({ POST: vi.fn() }));

vi.mock("./api-client", () => ({
  apiClient: apiMocks,
  csrfHeader: () => ({ "X-CSRF-Token": "kitesim-csrf" }),
  unwrap: async (result: { data?: unknown }) => result.data,
}));

import { importAdminKitesimAccounts } from "./admin-kitesim-api";

it.each([
  ["two fields", "owner@example.com----password", "owner@example.com----password"],
  ["phone suffix", "owner@example.com----password----15555550123", "owner@example.com----password"],
  ["many extra fields", "owner@example.com----password----" + "extra----".repeat(100), "owner@example.com----password"],
  ["empty extra fields", "owner@example.com----password--------", "owner@example.com----password"],
  ["empty password", "owner@example.com--------15555550123", "owner@example.com----"],
  ["missing delimiter", "owner@example.com", "owner@example.com"],
  [
    "multiple lines and CRLF",
    " owner@example.com ---- password ----15555550123\r\n\r\nsecond@example.com----secret\nthird@example.com----other----ignored\n",
    " owner@example.com ---- password \n\nsecond@example.com----secret\nthird@example.com----other\n",
  ],
])("prepares Kitesim import with %s before sending", async (_name, content, expected) => {
  const response = { imported: 1, queued: 1, failed: 0, errors: [] };
  apiMocks.POST.mockResolvedValueOnce({ data: response });

  await expect(importAdminKitesimAccounts(content)).resolves.toEqual(response);

  expect(apiMocks.POST).toHaveBeenLastCalledWith("/v1/admin/kitesim/accounts/imports", {
    body: { content: expected },
    params: { header: { "X-CSRF-Token": "kitesim-csrf" } },
  });
});
