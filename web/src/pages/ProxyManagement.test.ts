import { describe, expect, it } from "vitest";

import type { ProxyStatsResponse } from "@/lib/proxies-api";

import {
  proxyStatsFromResponse,
  proxyToggleCandidateCounts,
} from "./proxy-management-counts";

const crossFilteredStats: ProxyStatsResponse = {
  total: 1,
  countries: [
    { key: "JP", count: 1 },
    { key: "US", count: 2 },
  ],
  statuses: [
    { key: "abnormal", count: 1 },
    { key: "normal", count: 2 },
  ],
  pools: [
    { key: "resource", count: 2 },
    { key: "system", count: 1 },
  ],
  ipVersions: [
    { key: "ipv4", count: 2 },
    { key: "ipv6", count: 1 },
  ],
};

describe("proxy management filter counts", () => {
  it("uses each cross-filtered facet total instead of the matched list total", () => {
    expect(proxyStatsFromResponse(crossFilteredStats)).toMatchObject({
      status: { all: 3, abnormal: 1, normal: 2 },
      systemProxy: { all: 3, no: 2, yes: 1 },
      ipv6: { all: 3, no: 2, yes: 1 },
    });
  });

  it("keeps bulk enable and disable counts scoped to the selected status", () => {
    expect(proxyToggleCandidateCounts(crossFilteredStats, "all")).toEqual({
      disable: 1,
      enable: 0,
    });
    expect(proxyToggleCandidateCounts(crossFilteredStats, "disabled")).toEqual({
      disable: 0,
      enable: 1,
    });
    expect(proxyToggleCandidateCounts(crossFilteredStats, "abnormal")).toEqual({
      disable: 1,
      enable: 0,
    });
  });
});
