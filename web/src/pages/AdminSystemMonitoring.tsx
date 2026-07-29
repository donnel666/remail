import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  Button,
  Card,
  Input,
  Select,
  Spin,
  Table,
  Tag,
  Tooltip,
} from "@douyinfe/semi-ui";
import { VChart, VChartCore, type ISpec } from "@visactor/react-vchart";
import { semiDesignDark, semiDesignLight } from "@visactor/vchart-semi-theme";
import {
  Activity,
  CircleCheck,
  CircleOff,
  Cpu,
  Database,
  HardDrive,
  MemoryStick,
  RefreshCw,
  Search,
  Server,
  Timer,
  TriangleAlert,
  Zap,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { getIamErrorMessage } from "@/lib/iam-errors";
import { useIsMobile } from "@/hooks/use-is-mobile";
import {
  getSystemMonitoringSnapshot,
  metricSeriesKey,
  monitoringRates,
  type MonitoringMetric,
  type MonitoringQueue,
  type SystemMonitoringSnapshot,
} from "@/lib/system-monitoring-api";

type MonitoringStatus = "healthy" | "degraded" | "unavailable";
type MetricTypeFilter = "all" | MonitoringMetric["type"];
type Rates = ReturnType<typeof monitoringRates>;

interface HistoryPoint {
  cpu: number;
  heap: number;
  memory: number;
  tasks: number;
  time: string;
}

const CHART_OPTIONS = { mode: "desktop-browser" as const };
const EMPTY_RATES: Rates = { metrics: {}, queues: {}, tasksPerMinute: 0 };
const HISTORY_LIMIT = 36;

function applyVChartSemiTheme() {
  const dark =
    document.documentElement.classList.contains("dark") ||
    document.body.getAttribute("theme-mode") === "dark";
  const theme = dark ? semiDesignDark : semiDesignLight;
  const name = theme.name ?? (dark ? "semiDesignDark" : "semiDesignLight");
  if (!VChartCore.ThemeManager.themeExist(name)) {
    VChartCore.ThemeManager.registerTheme(name, theme);
  }
  VChartCore.ThemeManager.setCurrentTheme(name);
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const index = Math.min(
    Math.floor(Math.log(value) / Math.log(1024)),
    units.length - 1
  );
  const scaled = value / 1024 ** index;
  return `${scaled.toLocaleString(undefined, {
    maximumFractionDigits: scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2,
  })} ${units[index]}`;
}

function formatCount(value: number) {
  return Number.isFinite(value) ? value.toLocaleString() : "—";
}

function formatPercent(value: number) {
  return `${Math.max(0, value).toFixed(1)}%`;
}

function formatDuration(seconds: number) {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  if (seconds < 0.001) return `${(seconds * 1_000_000).toFixed(0)} μs`;
  if (seconds < 1) return `${(seconds * 1000).toFixed(seconds < 0.01 ? 2 : 1)} ms`;
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 2 : 1)} s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.floor(seconds % 60)}s`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
  return `${Math.floor(seconds / 86400)}d ${Math.floor((seconds % 86400) / 3600)}h`;
}

function formatMilliseconds(value: number, available = true) {
  if (!available || !Number.isFinite(value)) return "—";
  return value < 1 ? `${(value * 1000).toFixed(0)} μs` : `${value.toFixed(value < 10 ? 2 : 1)} ms`;
}

function formatRate(value: number | undefined) {
  if (value === undefined || !Number.isFinite(value)) return "—";
  return `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })}/min`;
}

function labelText(labels: Record<string, string>) {
  const entries = Object.entries(labels).sort(([left], [right]) =>
    left.localeCompare(right)
  );
  return entries.length
    ? entries.map(([key, value]) => `${key}=${value}`).join(" · ")
    : "—";
}

function overallStatus(snapshot: SystemMonitoringSnapshot): MonitoringStatus {
  const statuses = [
    snapshot.system.status,
    snapshot.mysql.status,
    snapshot.redis.status,
    snapshot.tasks.status,
    snapshot.application.status,
  ];
  if (statuses.includes("unavailable")) return "unavailable";
  if (statuses.includes("degraded") || !snapshot.tasks.workersReady) {
    return "degraded";
  }
  return "healthy";
}

function StatusBadge({
  status,
  t,
}: {
  status: MonitoringStatus;
  t: (key: string) => string;
}) {
  const meta = {
    healthy: {
      className: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
      icon: CircleCheck,
      label: t("Healthy"),
    },
    degraded: {
      className: "bg-amber-500/10 text-amber-700 dark:text-amber-300",
      icon: TriangleAlert,
      label: t("Degraded"),
    },
    unavailable: {
      className: "bg-rose-500/10 text-rose-700 dark:text-rose-300",
      icon: CircleOff,
      label: t("Unavailable"),
    },
  }[status];
  const Icon = meta.icon;
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium ${meta.className}`}
    >
      <Icon aria-hidden className="size-3.5" />
      {meta.label}
    </span>
  );
}

function Sparkline({ color, values }: { color: string; values: number[] }) {
  const points = values.length > 1 ? values : [0, 0];
  const minimum = Math.min(...points);
  const maximum = Math.max(...points);
  const range = maximum - minimum || 1;
  const polyline = points
    .map((value, index) => {
      const x = (index / Math.max(1, points.length - 1)) * 96 + 2;
      const y = 29 - ((value - minimum) / range) * 22;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  return (
    <svg aria-hidden className="h-9 w-24" preserveAspectRatio="none" viewBox="0 0 100 34">
      <polyline
        fill="none"
        points={polyline}
        stroke={color}
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="2"
      />
    </svg>
  );
}

function StatCard({
  color,
  detail,
  icon,
  label,
  values,
  value,
}: {
  color: string;
  detail: string;
  icon: ReactNode;
  label: string;
  values: number[];
  value: string;
}) {
  return (
    <div className="min-w-0 rounded-lg border border-border bg-card p-4 shadow-sm">
      <div className="mb-3 flex items-center justify-between gap-3">
        <span
          className="flex size-9 shrink-0 items-center justify-center rounded-lg"
          style={{ backgroundColor: `color-mix(in srgb, ${color} 14%, transparent)`, color }}
        >
          {icon}
        </span>
        <Sparkline color={color} values={values} />
      </div>
      <div className="text-xs font-medium text-[var(--semi-color-text-2)]">{label}</div>
      <div className="mt-1 font-mono-data text-2xl font-semibold tracking-tight text-[var(--semi-color-text-0)]">
        {value}
      </div>
      <div className="mt-1 truncate text-xs text-[var(--semi-color-text-2)]" title={detail}>
        {detail}
      </div>
    </div>
  );
}

function DetailCard({
  icon,
  items,
  status,
  title,
  t,
}: {
  icon: ReactNode;
  items: Array<[string, ReactNode]>;
  status: MonitoringStatus;
  title: string;
  t: (key: string) => string;
}) {
  return (
    <Card
      bodyStyle={{ padding: 16 }}
      bordered
      className="h-full !rounded-lg"
      headerLine
      title={
        <div className="flex w-full items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-2 font-semibold">
            {icon}
            <span className="truncate">{title}</span>
          </div>
          <StatusBadge status={status} t={t} />
        </div>
      }
    >
      <dl className="grid grid-cols-2 gap-x-5 gap-y-3">
        {items.map(([label, value]) => (
          <div className="min-w-0" key={label}>
            <dt className="truncate text-xs text-[var(--semi-color-text-2)]" title={label}>
              {label}
            </dt>
            <dd className="mt-0.5 min-w-0 break-words font-mono-data text-sm font-medium text-[var(--semi-color-text-0)]">
              {value}
            </dd>
          </div>
        ))}
      </dl>
    </Card>
  );
}

function chartSpec(
  values: Array<Record<string, string | number>>,
  colors: Record<string, string>,
  maximum?: number
): ISpec {
  return {
    animation: false,
    axes: [
      {
        label: maximum
          ? { formatMethod: (value: number) => `${value}%` }
          : undefined,
        max: maximum,
        min: 0,
        orient: "left",
      },
      { orient: "bottom" },
    ],
    color: { specified: colors },
    data: [{ id: "monitoring", values }],
    legends: { orient: "top", position: "end", visible: true },
    point: { visible: false },
    seriesField: "metric",
    type: "line",
    xField: "time",
    yField: "value",
  } as ISpec;
}

export default function AdminSystemMonitoring() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [snapshot, setSnapshot] = useState<SystemMonitoringSnapshot | null>(null);
  const [rates, setRates] = useState<Rates>(EMPTY_RATES);
  const [history, setHistory] = useState<HistoryPoint[]>([]);
  const [refreshSeconds, setRefreshSeconds] = useState(5);
  const [metricType, setMetricType] = useState<MetricTypeFilter>("all");
  const [metricSearch, setMetricSearch] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const previousSnapshot = useRef<SystemMonitoringSnapshot | null>(null);
  const requestSequence = useRef(0);

  const load = useCallback(async () => {
    const requestID = ++requestSequence.current;
    setLoading(true);
    try {
      const next = await getSystemMonitoringSnapshot();
      if (requestID !== requestSequence.current) return;
      const nextRates = monitoringRates(previousSnapshot.current, next);
      previousSnapshot.current = next;
      setRates(nextRates);
      setSnapshot(next);
      setHistory((current) => [
        ...current,
        {
          cpu: next.system.cpuPercent,
          heap: next.go.heapAllocBytes,
          memory: next.system.memoryPercent,
          tasks: nextRates.tasksPerMinute,
          time: new Date(next.sampledAt).toLocaleTimeString([], {
            hour: "2-digit",
            minute: "2-digit",
            second: "2-digit",
          }),
        },
      ].slice(-HISTORY_LIMIT));
      setError(null);
    } catch (loadError) {
      if (requestID !== requestSequence.current) return;
      setError(getIamErrorMessage(t, loadError, "Monitoring data unavailable."));
    } finally {
      if (requestID === requestSequence.current) {
        setLoading(false);
      }
    }
  }, [t]);

  useEffect(() => {
    void load();
    return () => {
      requestSequence.current += 1;
    };
  }, [load]);

  useEffect(() => {
    if (refreshSeconds <= 0) return undefined;
    const timer = window.setInterval(() => void load(), refreshSeconds * 1000);
    return () => window.clearInterval(timer);
  }, [load, refreshSeconds]);

  useEffect(() => {
    applyVChartSemiTheme();
    const observer = new MutationObserver(applyVChartSemiTheme);
    observer.observe(document.documentElement, {
      attributeFilter: ["class"],
      attributes: true,
    });
    observer.observe(document.body, {
      attributeFilter: ["theme-mode"],
      attributes: true,
    });
    return () => observer.disconnect();
  }, []);

  const totalPending = snapshot?.tasks.queues.reduce((total, queue) => total + queue.pending, 0) ?? 0;
  const totalActive = snapshot?.tasks.queues.reduce((total, queue) => total + queue.active, 0) ?? 0;
  const cpuHistory = history.map((point) => point.cpu);
  const memoryHistory = history.map((point) => point.memory);
  const heapHistory = history.map((point) => point.heap);
  const taskHistory = history.map((point) => point.tasks);

  const loadChartSpec = useMemo(() => {
    const cpu = t("CPU usage");
    const memory = t("Memory usage");
    return chartSpec(
      history.flatMap((point) => [
        { metric: cpu, time: point.time, value: point.cpu },
        { metric: memory, time: point.time, value: point.memory },
      ]),
      { [cpu]: "#3b82f6", [memory]: "#8b5cf6" },
      100
    );
  }, [history, t]);

  const taskChartSpec = useMemo(() => {
    const throughput = t("Task throughput");
    return chartSpec(
      history.map((point) => ({
        metric: throughput,
        time: point.time,
        value: point.tasks,
      })),
      { [throughput]: "#16a34a" }
    );
  }, [history, t]);

  const queueColumns = useMemo(
    () => [
      {
        dataIndex: "name",
        key: "name",
        title: t("Queue"),
        width: 220,
        render: (value: unknown, queue: MonitoringQueue) => (
          <div className="min-w-0">
            <code className="break-all text-xs font-semibold text-[var(--semi-color-text-0)]">
              {String(value)}
            </code>
            {queue.paused ? (
              <div className="mt-1 text-xs text-amber-600 dark:text-amber-300">{t("Paused")}</div>
            ) : null}
          </div>
        ),
      },
      {
        dataIndex: "status",
        key: "status",
        title: t("Status"),
        width: 118,
        render: (value: unknown) => (
          <StatusBadge status={value as MonitoringStatus} t={t} />
        ),
      },
      {
        dataIndex: "pending",
        key: "pending",
        title: t("Pending tasks"),
        width: 90,
        render: (value: unknown) => <span className="font-mono-data">{formatCount(Number(value))}</span>,
      },
      {
        dataIndex: "active",
        key: "active",
        title: t("Active tasks"),
        width: 82,
        render: (value: unknown) => <span className="font-mono-data">{formatCount(Number(value))}</span>,
      },
      {
        dataIndex: "retry",
        key: "retry",
        title: t("Retry tasks"),
        width: 82,
        render: (value: unknown) => <span className="font-mono-data">{formatCount(Number(value))}</span>,
      },
      {
        dataIndex: "scheduled",
        key: "scheduled",
        title: t("Scheduled tasks"),
        width: 100,
        render: (value: unknown) => <span className="font-mono-data">{formatCount(Number(value))}</span>,
      },
      {
        dataIndex: "latencySeconds",
        key: "latency",
        title: t("Latency"),
        width: 108,
        render: (value: unknown) => <span className="font-mono-data">{formatDuration(Number(value))}</span>,
      },
      {
        key: "throughput",
        title: t("Throughput"),
        width: 110,
        render: (_: unknown, queue: MonitoringQueue) => (
          <span className="font-mono-data">{formatRate(rates.queues[queue.name])}</span>
        ),
      },
      {
        key: "today",
        title: t("Today"),
        width: 140,
        render: (_: unknown, queue: MonitoringQueue) => (
          <div className="font-mono-data text-xs">
            <div>{formatCount(queue.processedToday)} {t("Processed today")}</div>
            <div className="text-[var(--semi-color-text-2)]">
              {formatCount(queue.failedToday)} {t("Failed today")}
            </div>
          </div>
        ),
      },
    ],
    [rates.queues, t]
  );

  const filteredMetrics = useMemo(() => {
    const search = metricSearch.trim().toLowerCase();
    return (snapshot?.application.series ?? []).filter((metric) => {
      if (metricType !== "all" && metric.type !== metricType) return false;
      if (!search) return true;
      return `${metric.name} ${metric.help} ${labelText(metric.labels)}`
        .toLowerCase()
        .includes(search);
    });
  }, [metricSearch, metricType, snapshot?.application.series]);

  const metricColumns = useMemo(
    () => [
      {
        dataIndex: "name",
        key: "metric",
        title: t("Metric"),
        width: "20%",
        render: (value: unknown, metric: MonitoringMetric) => (
          <Tooltip content={`${String(value)}: ${metric.help}`} position="topLeft">
            <div className="min-w-0 cursor-help">
              <code className="block truncate text-xs font-semibold text-[var(--semi-color-text-0)]">
                {String(value)}
              </code>
              <span className="mt-1 block truncate text-xs text-[var(--semi-color-text-2)]">
                {metric.help}
              </span>
            </div>
          </Tooltip>
        ),
      },
      {
        dataIndex: "type",
        key: "type",
        title: t("Type"),
        render: (value: unknown) => <Tag className="max-w-full" color="blue">{String(value)}</Tag>,
      },
      {
        dataIndex: "labels",
        key: "labels",
        title: t("Labels"),
        width: "15%",
        render: (value: unknown) => {
          const text = labelText(value as Record<string, string>);
          return (
            <Tooltip content={text} position="topLeft">
              <code className="block truncate text-xs text-[var(--semi-color-text-1)]">{text}</code>
            </Tooltip>
          );
        },
      },
      {
        key: "current",
        title: t("Current"),
        render: (_: unknown, metric: MonitoringMetric) => (
          <span className="font-mono-data">
            {formatCount(metric.type === "histogram" ? metric.count : metric.value)}
          </span>
        ),
      },
      {
        dataIndex: "average",
        key: "average",
        title: t("Average"),
        render: (value: unknown, metric: MonitoringMetric) => (
          <span className="font-mono-data">
            {metric.type === "histogram" && metric.count > 0
              ? formatDuration(Number(value))
              : "—"}
          </span>
        ),
      },
      {
        dataIndex: "p50",
        key: "p50",
        title: "P50",
        render: (value: unknown, metric: MonitoringMetric) => (
          <span className="font-mono-data">
            {metric.type === "histogram" && metric.count > 0
              ? formatDuration(Number(value))
              : "—"}
          </span>
        ),
      },
      {
        dataIndex: "p95",
        key: "p95",
        title: "P95",
        render: (value: unknown, metric: MonitoringMetric) => (
          <span className="font-mono-data">
            {metric.type === "histogram" && metric.count > 0
              ? formatDuration(Number(value))
              : "—"}
          </span>
        ),
      },
      {
        key: "rate",
        title: t("Rate per minute"),
        render: (_: unknown, metric: MonitoringMetric) => (
          <span className="font-mono-data">{formatRate(rates.metrics[metricSeriesKey(metric)])}</span>
        ),
      },
    ],
    [rates.metrics, t]
  );

  const status = snapshot ? overallStatus(snapshot) : "unavailable";
  const lastUpdated = snapshot
    ? new Date(snapshot.sampledAt).toLocaleString()
    : t("Not updated");

  return (
    <div className="console-content-width console-dashboard-page min-h-full py-5">
      <header className="mb-4 flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-2xl font-semibold text-[var(--semi-color-text-0)]">
              {t("System Monitoring")}
            </h1>
            <StatusBadge status={status} t={t} />
          </div>
          <div
            aria-live="polite"
            className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--semi-color-text-2)]"
          >
            <span>{t("Last updated")}: {lastUpdated}</span>
            <span>{refreshSeconds > 0 ? t("Live") : t("Paused")}</span>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Select
            aria-label={t("Auto refresh")}
            className="min-w-32 !h-11"
            onChange={(value) => setRefreshSeconds(Number(value))}
            optionList={[
              { label: t("Paused"), value: 0 },
              { label: t("Every 5 seconds"), value: 5 },
              { label: t("Every 10 seconds"), value: 10 },
              { label: t("Every 30 seconds"), value: 30 },
            ]}
            style={{ height: 44 }}
            value={refreshSeconds}
          />
          <Tooltip content={t("Refresh")}>
            <Button
              aria-label={t("Refresh")}
              className="!h-11 !w-11"
              icon={<RefreshCw aria-hidden className="size-4" />}
              loading={loading}
              onClick={() => void load()}
              theme="outline"
              type="tertiary"
            />
          </Tooltip>
        </div>
      </header>

      {error ? (
        <div
          className="mb-4 flex flex-col gap-3 rounded-lg border border-rose-500/30 bg-rose-500/5 p-3 text-sm text-rose-700 dark:text-rose-300 sm:flex-row sm:items-center sm:justify-between"
          role="alert"
        >
          <span>{error}</span>
          <Button onClick={() => void load()} size="small" theme="outline" type="danger">
            {t("Retry")}
          </Button>
        </div>
      ) : null}

      {!snapshot ? (
        <div className="flex min-h-[420px] items-center justify-center" role="status">
          <div className="flex flex-col items-center gap-3 text-sm text-[var(--semi-color-text-2)]">
            <Spin size="large" />
            {t("Loading monitoring data...")}
          </div>
        </div>
      ) : (
        <>
          <section aria-label={t("System summary")} className="mb-4 grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
            <StatCard
              color="#3b82f6"
              detail={`${t("Load")}: ${snapshot.system.load1.toFixed(2)} / ${snapshot.system.load5.toFixed(2)} / ${snapshot.system.load15.toFixed(2)} · ${snapshot.go.numCpu} ${t("cores")}`}
              icon={<Cpu aria-hidden className="size-4" />}
              label={t("Host CPU")}
              value={formatPercent(snapshot.system.cpuPercent)}
              values={cpuHistory}
            />
            <StatCard
              color="#8b5cf6"
              detail={`${formatBytes(snapshot.system.memoryUsedBytes)} / ${formatBytes(snapshot.system.memoryTotalBytes)} · ${t("Uptime")} ${formatDuration(snapshot.system.uptimeSeconds)}`}
              icon={<MemoryStick aria-hidden className="size-4" />}
              label={t("System memory")}
              value={formatPercent(snapshot.system.memoryPercent)}
              values={memoryHistory}
            />
            <StatCard
              color="#f97316"
              detail={`${t("Heap")} ${formatBytes(snapshot.go.heapAllocBytes)} · ${t("Reserved")} ${formatBytes(snapshot.go.sysBytes)}`}
              icon={<HardDrive aria-hidden className="size-4" />}
              label={t("Go memory")}
              value={formatBytes(snapshot.go.heapAllocBytes)}
              values={heapHistory}
            />
            <StatCard
              color="#16a34a"
              detail={`${formatCount(totalPending)} ${t("pending")} · ${formatCount(totalActive)} ${t("active")}`}
              icon={<Zap aria-hidden className="size-4" />}
              label={t("Task throughput")}
              value={formatRate(history.length > 1 ? rates.tasksPerMinute : undefined)}
              values={taskHistory}
            />
          </section>

          <section className="mb-4 grid grid-cols-1 gap-4 xl:grid-cols-3">
            <Card
              bodyStyle={{ padding: 8 }}
              bordered
              className="!rounded-lg xl:col-span-2"
              headerLine
              title={
                <div className="flex items-center gap-2 font-semibold">
                  <Activity aria-hidden className="size-4" />
                  {t("Real-time load")}
                  <span className="text-xs font-normal text-[var(--semi-color-text-2)]">{t("Browser rolling window")}</span>
                </div>
              }
            >
              <div className="h-72" aria-label={t("CPU and memory usage chart")}>
                <VChart options={CHART_OPTIONS} spec={loadChartSpec} />
              </div>
              <span className="sr-only" aria-live="polite">
                {t("CPU usage")}: {formatPercent(snapshot.system.cpuPercent)}; {t("Memory usage")}: {formatPercent(snapshot.system.memoryPercent)}
              </span>
            </Card>
            <Card
              bodyStyle={{ padding: 8 }}
              bordered
              className="!rounded-lg"
              headerLine
              title={
                <div className="flex items-center gap-2 font-semibold">
                  <Timer aria-hidden className="size-4" />
                  {t("Queue throughput")}
                </div>
              }
            >
              <div className="h-72" aria-label={t("Task throughput chart")}>
                <VChart options={CHART_OPTIONS} spec={taskChartSpec} />
              </div>
              <span className="sr-only" aria-live="polite">
                {t("Task throughput")}: {formatRate(rates.tasksPerMinute)}
              </span>
            </Card>
          </section>

          <section className="mb-4 grid grid-cols-1 gap-4 xl:grid-cols-3">
            <DetailCard
              icon={<Cpu aria-hidden className="size-4" />}
              items={[
                [t("Version"), snapshot.go.version],
                [t("CPU / GOMAXPROCS"), `${snapshot.go.numCpu} / ${snapshot.go.gomaxprocs}`],
                [t("Goroutines"), formatCount(snapshot.go.goroutines)],
                [t("Process uptime"), formatDuration(snapshot.go.processUptimeSeconds)],
                [t("Heap allocated"), formatBytes(snapshot.go.heapAllocBytes)],
                [t("Go reserved memory"), formatBytes(snapshot.go.sysBytes)],
                [t("Heap objects"), formatCount(snapshot.go.heapObjects)],
                [t("GC cycles"), formatCount(snapshot.go.gcCycles)],
                [t("Last GC pause"), formatDuration(snapshot.go.lastGcPauseSeconds)],
                [t("GC CPU"), formatPercent(snapshot.go.gcCpuFraction * 100)],
              ]}
              status="healthy"
              t={t}
              title={t("Go runtime")}
            />
            <DetailCard
              icon={<Database aria-hidden className="size-4" />}
              items={[
                [t("Ping"), formatMilliseconds(snapshot.mysql.pingMilliseconds, snapshot.mysql.status !== "unavailable")],
                [t("Open connections"), formatCount(snapshot.mysql.openConnections)],
                [t("In use"), formatCount(snapshot.mysql.inUseConnections)],
                [t("Idle"), formatCount(snapshot.mysql.idleConnections)],
                [t("Maximum"), formatCount(snapshot.mysql.maxOpenConnections)],
                [t("Wait count"), formatCount(snapshot.mysql.waitCount)],
                [t("Wait duration"), formatMilliseconds(snapshot.mysql.waitDurationMilliseconds)],
                [t("Idle closes"), formatCount(snapshot.mysql.maxIdleClosed + snapshot.mysql.maxIdleTimeClosed)],
                [t("Lifetime closes"), formatCount(snapshot.mysql.maxLifetimeClosed)],
              ]}
              status={snapshot.mysql.status}
              t={t}
              title="MySQL"
            />
            <DetailCard
              icon={<Server aria-hidden className="size-4" />}
              items={[
                [t("Ping"), formatMilliseconds(snapshot.redis.pingMilliseconds, snapshot.redis.status !== "unavailable")],
                [t("Memory used"), formatBytes(snapshot.redis.usedMemoryBytes)],
                [t("Memory limit"), snapshot.redis.maxMemoryBytes ? formatBytes(snapshot.redis.maxMemoryBytes) : t("Unlimited")],
                [t("Connected clients"), formatCount(snapshot.redis.connectedClients)],
                [t("Operations per second"), formatCount(snapshot.redis.operationsPerSecond)],
                [t("Cache hit rate"), formatPercent(snapshot.redis.hitRatePercent)],
                [t("Evicted keys"), formatCount(snapshot.redis.evictedKeys)],
                [t("Fragmentation ratio"), snapshot.redis.fragmentationRatio.toFixed(2)],
                [t("Pool connections"), `${snapshot.redis.poolIdleConnections} / ${snapshot.redis.poolTotalConnections}`],
                [t("Pool timeouts"), formatCount(snapshot.redis.poolTimeouts)],
              ]}
              status={snapshot.redis.status}
              t={t}
              title="Redis"
            />
          </section>

          <section className="mb-4 overflow-hidden rounded-lg border border-border bg-card">
            <div className="flex flex-col gap-3 border-b border-border p-4 md:flex-row md:items-center md:justify-between">
              <div className="flex items-center gap-2">
                <Zap aria-hidden className="size-4" />
                <h2 className="font-semibold text-[var(--semi-color-text-0)]">{t("Task queues")}</h2>
                <StatusBadge status={snapshot.tasks.status} t={t} />
              </div>
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-[var(--semi-color-text-2)]">
                <span>{t("Background workers")}: <strong className="font-mono-data text-[var(--semi-color-text-0)]">{snapshot.tasks.backgroundWorkers.active}/{snapshot.tasks.backgroundWorkers.limit}</strong></span>
                <span>{t("Maximum")}: <strong className="font-mono-data text-[var(--semi-color-text-0)]">{snapshot.tasks.backgroundWorkers.maximum}</strong></span>
                <span>{t("Workers ready")}: <strong className="text-[var(--semi-color-text-0)]">{snapshot.tasks.workersReady ? t("Yes") : t("No")}</strong></span>
              </div>
            </div>
            {isMobile ? (
              <div className="divide-y divide-border">
                {snapshot.tasks.queues.map((queue) => (
                  <div className="p-4" key={queue.name}>
                    <div className="flex items-start justify-between gap-3">
                      <code className="min-w-0 break-all text-xs font-semibold text-[var(--semi-color-text-0)]">
                        {queue.name}
                      </code>
                      <StatusBadge status={queue.status} t={t} />
                    </div>
                    <dl className="mt-3 grid grid-cols-3 gap-x-3 gap-y-3">
                      {[
                        [t("Pending tasks"), formatCount(queue.pending)],
                        [t("Active tasks"), formatCount(queue.active)],
                        [t("Retry tasks"), formatCount(queue.retry)],
                        [t("Scheduled tasks"), formatCount(queue.scheduled)],
                        [t("Latency"), formatDuration(queue.latencySeconds)],
                        [t("Throughput"), formatRate(rates.queues[queue.name])],
                      ].map(([label, value]) => (
                        <div className="min-w-0" key={label}>
                          <dt className="truncate text-[11px] text-[var(--semi-color-text-2)]">{label}</dt>
                          <dd className="mt-0.5 truncate font-mono-data text-xs font-medium text-[var(--semi-color-text-0)]">
                            {value}
                          </dd>
                        </div>
                      ))}
                    </dl>
                    <div className="mt-3 text-xs text-[var(--semi-color-text-2)]">
                      <span className="font-mono-data text-[var(--semi-color-text-0)]">{formatCount(queue.processedToday)}</span> {t("Processed today")}
                      <span className="mx-2">·</span>
                      <span className="font-mono-data text-[var(--semi-color-text-0)]">{formatCount(queue.failedToday)}</span> {t("Failed today")}
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div className="overflow-x-auto">
                <Table
                  columns={queueColumns as never}
                  dataSource={snapshot.tasks.queues}
                  pagination={false}
                  rowKey="name"
                  scroll={{ x: "max(100%, 1050px)" }}
                  size="small"
                />
              </div>
            )}
          </section>

          <section className="overflow-hidden rounded-lg border border-border bg-card">
            <div className="flex flex-col gap-3 border-b border-border p-4 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex items-center gap-2">
                <Database aria-hidden className="size-4" />
                <h2 className="font-semibold text-[var(--semi-color-text-0)]">{t("Core tuning metrics")}</h2>
                <span className="font-mono-data text-xs text-[var(--semi-color-text-2)]">{filteredMetrics.length}</span>
              </div>
              <div className="flex flex-col gap-2 sm:flex-row">
                <Select
                  aria-label={t("Metric type")}
                  className="!h-11"
                  onChange={(value) => setMetricType(String(value) as MetricTypeFilter)}
                  optionList={[
                    { label: t("All types"), value: "all" },
                    { label: "counter", value: "counter" },
                    { label: "gauge", value: "gauge" },
                    { label: "histogram", value: "histogram" },
                    { label: "untyped", value: "untyped" },
                  ]}
                  style={{ minWidth: 132 }}
                  value={metricType}
                />
                <Input
                  aria-label={t("Search metrics")}
                  className="!h-11"
                  id="monitoring-metric-search"
                  name="monitoring-metric-search"
                  onChange={setMetricSearch}
                  placeholder={t("Search metrics")}
                  prefix={<Search aria-hidden className="size-4 text-[var(--semi-color-text-2)]" />}
                  showClear
                  style={{ minWidth: 240 }}
                  value={metricSearch}
                />
              </div>
            </div>
            <div className="overflow-x-auto">
              <Table
                className="[&_.semi-table]:table-fixed"
                columns={metricColumns as never}
                dataSource={filteredMetrics}
                pagination={filteredMetrics.length > 20 ? { pageSize: 20 } : false}
                rowKey={(metric) => metric ? metricSeriesKey(metric) : ""}
                size="small"
              />
            </div>
          </section>
        </>
      )}
    </div>
  );
}
