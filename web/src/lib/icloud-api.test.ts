// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api-client")>();
  const { default: createClient } = await import("openapi-fetch");
  return { ...actual, apiClient: createClient({ baseUrl: "https://remail.test", fetch: (request: Request) => globalThis.fetch(request) }) };
});
import { applyICloudCommand, applyICloudCommandByFilter, getICloudImport, importICloudResources, listICloudResources } from "./icloud-api";

describe("owned Apple API", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("sends the whole filter through the owned batch endpoint with write protection", async () => {
    const requests: Request[] = [];
    vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
      requests.push(request);
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    document.cookie = "csrf_token=csrf";
    await applyICloudCommandByFilter("delete", { suffix: "outlook.com", status: "abnormal", forSale: false });
    expect(requests[0].url).toBe("https://remail.test/v1/icloud/resources/actions");
    expect(requests[0].headers.get("X-CSRF-Token")).toBe("csrf");
    expect(requests[0].headers.get("Idempotency-Key")).toBeTruthy();
    expect(await requests[0].json()).toEqual({ command: "delete", filter: { suffix: "outlook.com", status: "abnormal", forSale: false } });
  });
  it("uploads only the file and protects writes with CSRF and idempotency", async () => {
    const requests: Request[] = [];
    vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
      requests.push(request);
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    }));
    document.cookie = "csrf_token=csrf";
    const file = new File(["apple@example.com----pw----https://codes.example.com/token"], "accounts.txt");
    await importICloudResources(file, "retry-key", "challenge");
    expect(requests[0].url).toContain("/v1/icloud/resources/imports");
    expect(requests[0].headers.get("X-CSRF-Token")).toBe("csrf");
    expect(requests[0].headers.get("X-Turnstile-Token")).toBe("challenge");
    expect(requests[0].headers.get("Idempotency-Key")).toBe("retry-key");
    const body = await requests[0].text();
    expect(body).toContain('name="file"');
    expect(body).not.toContain('name="expireAt"');
    expect(body).not.toContain('name="ownerId"');
    await applyICloudCommand("validate", [{ id: 8, version: 3 }]);
    expect(await requests[1].json()).toEqual({ command: "validate", resources: [{ id: 8, version: 3 }] });
    expect(requests[1].headers.get("Idempotency-Key")).toBeTruthy();
    await getICloudImport(8);
    await listICloudResources({ search: "icloud", offset: 100, limit: 100 });
    expect(requests[2].url).toContain("/v1/icloud/resources/imports/8");
    expect(requests[3].url).toContain("offset=100");
    expect(requests.every((request) => !request.url.includes("/admin/"))).toBe(true);
  });
});
