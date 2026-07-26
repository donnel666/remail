import { describe, expect, it } from "vitest";

import { monitoringRates, type MonitoringMetric } from "./system-monitoring-api";

function counter(value: number): MonitoringMetric {
  return {
    average: 0,
    count: 0,
    help: "test",
    labels: { result: "ok" },
    name: "remail_test_total",
    p50: 0,
    p95: 0,
    sum: 0,
    type: "counter",
    value,
  };
}

describe("monitoringRates", () => {
  it("calculates queue and application throughput from cumulative counters", () => {
    const previous = {
      sampledAt: "2026-07-26T00:00:00Z",
      tasks: { queues: [{ name: "default", processedTotal: 100 }] },
      application: { series: [counter(40)] },
    };
    const current = {
      sampledAt: "2026-07-26T00:00:10Z",
      tasks: { queues: [{ name: "default", processedTotal: 105 }] },
      application: { series: [counter(42)] },
    };

    const rates = monitoringRates(previous, current);

    expect(rates.queues.default).toBeCloseTo(30);
    expect(rates.tasksPerMinute).toBeCloseTo(30);
    expect(Object.values(rates.metrics)[0]).toBeCloseTo(12);
  });
});
