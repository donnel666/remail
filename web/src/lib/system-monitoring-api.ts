import { apiClient, unwrap } from "./api-client";
import type { components } from "./openapi/schema";

export type SystemMonitoringSnapshot = components["schemas"]["AdminMonitoringSnapshot"];
export type MonitoringQueue = components["schemas"]["MonitoringQueueStats"];
export type MonitoringMetric = components["schemas"]["MonitoringMetricSeries"];

type RateSnapshot = {
  sampledAt: string;
  tasks: { queues: Array<Pick<MonitoringQueue, "name" | "processedTotal">> };
  application: { series: MonitoringMetric[] };
};

export async function getSystemMonitoringSnapshot() {
  return unwrap<SystemMonitoringSnapshot>(
    await apiClient.GET("/v1/admin/monitoring")
  );
}

export function metricSeriesKey(metric: MonitoringMetric) {
  const labels = Object.entries(metric.labels)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => `${key}=${value}`)
    .join(",");
  return `${metric.name}|${labels}`;
}

export function monitoringRates(
  previous: RateSnapshot | null,
  current: RateSnapshot
) {
  const queues: Record<string, number> = {};
  const metrics: Record<string, number> = {};
  if (!previous) return { queues, metrics, tasksPerMinute: 0 };

  const elapsedMinutes =
    (new Date(current.sampledAt).getTime() -
      new Date(previous.sampledAt).getTime()) /
    60_000;
  if (!Number.isFinite(elapsedMinutes) || elapsedMinutes <= 0) {
    return { queues, metrics, tasksPerMinute: 0 };
  }

  const previousQueues = new Map(
    previous.tasks.queues.map((queue) => [queue.name, queue.processedTotal])
  );
  for (const queue of current.tasks.queues) {
    const before = previousQueues.get(queue.name);
    if (before === undefined || queue.processedTotal < before) continue;
    queues[queue.name] = (queue.processedTotal - before) / elapsedMinutes;
  }

  const previousMetrics = new Map(
    previous.application.series.map((metric) => [metricSeriesKey(metric), metric])
  );
  for (const metric of current.application.series) {
    if (metric.type !== "counter" && metric.type !== "histogram") continue;
    const before = previousMetrics.get(metricSeriesKey(metric));
    if (!before) continue;
    const currentValue = metric.type === "histogram" ? metric.count : metric.value;
    const previousValue = before.type === "histogram" ? before.count : before.value;
    if (currentValue < previousValue) continue;
    metrics[metricSeriesKey(metric)] = (currentValue - previousValue) / elapsedMinutes;
  }

  return {
    queues,
    metrics,
    tasksPerMinute: Object.values(queues).reduce((total, rate) => total + rate, 0),
  };
}
