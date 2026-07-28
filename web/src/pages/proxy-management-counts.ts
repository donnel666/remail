import type { ProxyStatsResponse } from "@/lib/proxies-api";

export type StatusFilter =
  | "all"
  | "pending"
  | "checking"
  | "normal"
  | "abnormal"
  | "disabled"
  | "expired";

const emptyStats = {
  ipv6: { all: 0, no: 0, yes: 0 },
  systemProxy: { all: 0, no: 0, yes: 0 },
  status: {
    all: 0,
    abnormal: 0,
    pending: 0,
    checking: 0,
    disabled: 0,
    expired: 0,
    normal: 0,
  },
};

function countOf(items: ProxyStatsResponse["statuses"], key: string) {
  return items.find((item) => item.key === key)?.count ?? 0;
}

function sumCounts(items: ProxyStatsResponse["statuses"]) {
  return items.reduce((total, item) => total + item.count, 0);
}

export function proxyStatsFromResponse(stats?: ProxyStatsResponse | null) {
  if (!stats) return emptyStats;
  const statusTotal = sumCounts(stats.statuses);
  const poolTotal = sumCounts(stats.pools);
  const ipVersionTotal = sumCounts(stats.ipVersions);
  const systemCount = countOf(stats.pools, "system");
  const ipv6Count = countOf(stats.ipVersions, "ipv6");
  return {
    ipv6: {
      all: ipVersionTotal,
      no: Math.max(ipVersionTotal - ipv6Count, 0),
      yes: ipv6Count,
    },
    systemProxy: {
      all: poolTotal,
      no: Math.max(poolTotal - systemCount, 0),
      yes: systemCount,
    },
    status: {
      all: statusTotal,
      abnormal: countOf(stats.statuses, "abnormal"),
      pending: countOf(stats.statuses, "pending"),
      checking: countOf(stats.statuses, "checking"),
      disabled: countOf(stats.statuses, "disabled"),
      expired: countOf(stats.statuses, "expired"),
      normal: countOf(stats.statuses, "normal"),
    },
  };
}

export function proxyToggleCandidateCounts(
  stats: ProxyStatsResponse | null,
  statusFilter: StatusFilter
) {
  const matched = stats?.total ?? 0;
  const disabled =
    statusFilter === "all"
      ? countOf(stats?.statuses ?? [], "disabled")
      : statusFilter === "disabled"
        ? matched
        : 0;
  return { disable: Math.max(matched - disabled, 0), enable: disabled };
}
