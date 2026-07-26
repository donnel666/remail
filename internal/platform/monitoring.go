package platform

import (
	"context"
	"errors"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

var processStartedAt = time.Now()

type MonitoringSnapshot struct {
	SampledAt   time.Time             `json:"sampledAt"`
	System      MonitoringSystem      `json:"system"`
	Go          MonitoringGo          `json:"go"`
	MySQL       MonitoringMySQL       `json:"mysql"`
	Redis       MonitoringRedis       `json:"redis"`
	Tasks       MonitoringTasks       `json:"tasks"`
	Application MonitoringApplication `json:"application"`
}

type MonitoringSystem struct {
	Status           string  `json:"status"`
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryPercent    float64 `json:"memoryPercent"`
	MemoryUsedBytes  uint64  `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64  `json:"memoryTotalBytes"`
	Load1            float64 `json:"load1"`
	Load5            float64 `json:"load5"`
	Load15           float64 `json:"load15"`
	UptimeSeconds    uint64  `json:"uptimeSeconds"`
}

type MonitoringGo struct {
	Version              string  `json:"version"`
	NumCPU               int     `json:"numCpu"`
	GOMAXPROCS           int     `json:"gomaxprocs"`
	Goroutines           int     `json:"goroutines"`
	CgoCalls             int64   `json:"cgoCalls"`
	ProcessUptimeSeconds uint64  `json:"processUptimeSeconds"`
	HeapAllocBytes       uint64  `json:"heapAllocBytes"`
	HeapInUseBytes       uint64  `json:"heapInUseBytes"`
	HeapObjects          uint64  `json:"heapObjects"`
	StackInUseBytes      uint64  `json:"stackInUseBytes"`
	SysBytes             uint64  `json:"sysBytes"`
	NextGCBytes          uint64  `json:"nextGcBytes"`
	GCCycles             uint32  `json:"gcCycles"`
	LastGCPauseSeconds   float64 `json:"lastGcPauseSeconds"`
	GCCPUFraction        float64 `json:"gcCpuFraction"`
}

type MonitoringMySQL struct {
	Status                   string  `json:"status"`
	PingMilliseconds         float64 `json:"pingMilliseconds"`
	OpenConnections          int     `json:"openConnections"`
	InUseConnections         int     `json:"inUseConnections"`
	IdleConnections          int     `json:"idleConnections"`
	MaxOpenConnections       int     `json:"maxOpenConnections"`
	WaitCount                int64   `json:"waitCount"`
	WaitDurationMilliseconds float64 `json:"waitDurationMilliseconds"`
	MaxIdleClosed            int64   `json:"maxIdleClosed"`
	MaxIdleTimeClosed        int64   `json:"maxIdleTimeClosed"`
	MaxLifetimeClosed        int64   `json:"maxLifetimeClosed"`
}

type MonitoringRedis struct {
	Status               string  `json:"status"`
	PingMilliseconds     float64 `json:"pingMilliseconds"`
	UsedMemoryBytes      uint64  `json:"usedMemoryBytes"`
	MaxMemoryBytes       uint64  `json:"maxMemoryBytes"`
	ConnectedClients     uint64  `json:"connectedClients"`
	OperationsPerSecond  uint64  `json:"operationsPerSecond"`
	KeyspaceHits         uint64  `json:"keyspaceHits"`
	KeyspaceMisses       uint64  `json:"keyspaceMisses"`
	HitRatePercent       float64 `json:"hitRatePercent"`
	EvictedKeys          uint64  `json:"evictedKeys"`
	FragmentationRatio   float64 `json:"fragmentationRatio"`
	PoolHits             uint32  `json:"poolHits"`
	PoolMisses           uint32  `json:"poolMisses"`
	PoolTimeouts         uint32  `json:"poolTimeouts"`
	PoolTotalConnections uint32  `json:"poolTotalConnections"`
	PoolIdleConnections  uint32  `json:"poolIdleConnections"`
	PoolStaleConnections uint32  `json:"poolStaleConnections"`
}

type MonitoringTasks struct {
	Status            string                      `json:"status"`
	WorkersReady      bool                        `json:"workersReady"`
	BackgroundWorkers MonitoringBackgroundWorkers `json:"backgroundWorkers"`
	Queues            []MonitoringQueue           `json:"queues"`
}

type MonitoringBackgroundWorkers struct {
	Limit          int     `json:"limit"`
	Active         int     `json:"active"`
	Maximum        int     `json:"maximum"`
	CPUPercent     float64 `json:"cpuPercent"`
	MemoryPercent  float64 `json:"memoryPercent"`
	MetricsHealthy bool    `json:"metricsHealthy"`
}

type MonitoringQueue struct {
	Name             string  `json:"name"`
	Status           string  `json:"status"`
	Paused           bool    `json:"paused"`
	Pending          int     `json:"pending"`
	Active           int     `json:"active"`
	Scheduled        int     `json:"scheduled"`
	Retry            int     `json:"retry"`
	Archived         int     `json:"archived"`
	ProcessedToday   int     `json:"processedToday"`
	FailedToday      int     `json:"failedToday"`
	ProcessedTotal   int     `json:"processedTotal"`
	FailedTotal      int     `json:"failedTotal"`
	LatencySeconds   float64 `json:"latencySeconds"`
	MemoryUsageBytes int64   `json:"memoryUsageBytes"`
}

type MonitoringApplication struct {
	Status string             `json:"status"`
	Series []MonitoringMetric `json:"series"`
}

type MonitoringMetric struct {
	Name    string            `json:"name"`
	Help    string            `json:"help"`
	Type    string            `json:"type"`
	Labels  map[string]string `json:"labels"`
	Value   float64           `json:"value"`
	Count   uint64            `json:"count"`
	Sum     float64           `json:"sum"`
	Average float64           `json:"average"`
	P50     float64           `json:"p50"`
	P95     float64           `json:"p95"`
}

func (p *Platform) MonitoringSnapshot(ctx context.Context) MonitoringSnapshot {
	snapshot := MonitoringSnapshot{SampledAt: time.Now()}
	snapshot.System = p.monitoringSystem(ctx)
	snapshot.Go = monitoringGo()
	snapshot.MySQL = p.monitoringMySQL(ctx)
	snapshot.Redis = p.monitoringRedis(ctx)
	snapshot.Tasks = p.monitoringTasks()
	snapshot.Application = monitoringApplication()
	return snapshot
}

func (p *Platform) monitoringSystem(ctx context.Context) MonitoringSystem {
	result := MonitoringSystem{Status: "degraded"}
	cpuOK := false
	if p != nil && p.BackgroundLoad != nil {
		loadSnapshot := p.BackgroundLoad.Snapshot()
		if loadSnapshot.CPUValid && time.Since(loadSnapshot.SampledAt) < 10*time.Second {
			result.CPUPercent = loadSnapshot.CPUPercent
			cpuOK = true
		}
	}
	if !cpuOK {
		if values, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(values) > 0 {
			result.CPUPercent = values[0]
			cpuOK = validSystemPercent(values[0])
		}
	}
	memoryOK := false
	if stats, err := mem.VirtualMemoryWithContext(ctx); err == nil && stats != nil {
		result.MemoryPercent = stats.UsedPercent
		result.MemoryUsedBytes = stats.Used
		result.MemoryTotalBytes = stats.Total
		memoryOK = true
	}
	if averages, err := load.AvgWithContext(ctx); err == nil && averages != nil {
		result.Load1 = averages.Load1
		result.Load5 = averages.Load5
		result.Load15 = averages.Load15
	}
	if uptime, err := host.UptimeWithContext(ctx); err == nil {
		result.UptimeSeconds = uptime
	}
	if memoryOK && cpuOK {
		result.Status = "healthy"
	}
	return result
}

func monitoringGo() MonitoringGo {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	lastPause := uint64(0)
	if stats.NumGC > 0 {
		lastPause = stats.PauseNs[(stats.NumGC+255)%256]
	}
	return MonitoringGo{
		Version:              runtime.Version(),
		NumCPU:               runtime.NumCPU(),
		GOMAXPROCS:           runtime.GOMAXPROCS(0),
		Goroutines:           runtime.NumGoroutine(),
		CgoCalls:             runtime.NumCgoCall(),
		ProcessUptimeSeconds: uint64(time.Since(processStartedAt).Seconds()),
		HeapAllocBytes:       stats.HeapAlloc,
		HeapInUseBytes:       stats.HeapInuse,
		HeapObjects:          stats.HeapObjects,
		StackInUseBytes:      stats.StackInuse,
		SysBytes:             stats.Sys,
		NextGCBytes:          stats.NextGC,
		GCCycles:             stats.NumGC,
		LastGCPauseSeconds:   float64(lastPause) / float64(time.Second),
		GCCPUFraction:        stats.GCCPUFraction,
	}
}

func (p *Platform) monitoringMySQL(ctx context.Context) MonitoringMySQL {
	result := MonitoringMySQL{Status: "unavailable"}
	if p == nil || p.SQLDB == nil {
		return result
	}
	stats := p.SQLDB.Stats()
	result.OpenConnections = stats.OpenConnections
	result.InUseConnections = stats.InUse
	result.IdleConnections = stats.Idle
	result.MaxOpenConnections = stats.MaxOpenConnections
	result.WaitCount = stats.WaitCount
	result.WaitDurationMilliseconds = float64(stats.WaitDuration) / float64(time.Millisecond)
	result.MaxIdleClosed = stats.MaxIdleClosed
	result.MaxIdleTimeClosed = stats.MaxIdleTimeClosed
	result.MaxLifetimeClosed = stats.MaxLifetimeClosed
	startedAt := time.Now()
	if err := p.SQLDB.PingContext(ctx); err == nil {
		result.Status = "healthy"
		result.PingMilliseconds = float64(time.Since(startedAt)) / float64(time.Millisecond)
	}
	return result
}

func (p *Platform) monitoringRedis(ctx context.Context) MonitoringRedis {
	result := MonitoringRedis{Status: "unavailable"}
	if p == nil || p.Redis == nil {
		return result
	}
	pool := p.Redis.PoolStats()
	result.PoolHits = pool.Hits
	result.PoolMisses = pool.Misses
	result.PoolTimeouts = pool.Timeouts
	result.PoolTotalConnections = pool.TotalConns
	result.PoolIdleConnections = pool.IdleConns
	result.PoolStaleConnections = pool.StaleConns

	startedAt := time.Now()
	if err := p.Redis.Ping(ctx).Err(); err != nil {
		return result
	}
	result.Status = "healthy"
	result.PingMilliseconds = float64(time.Since(startedAt)) / float64(time.Millisecond)
	info, err := p.Redis.Info(ctx, "memory", "clients", "stats").Result()
	if err != nil {
		result.Status = "degraded"
		return result
	}
	values := parseRedisInfo(info)
	result.UsedMemoryBytes = parseRedisUint(values["used_memory"])
	result.MaxMemoryBytes = parseRedisUint(values["maxmemory"])
	result.ConnectedClients = parseRedisUint(values["connected_clients"])
	result.OperationsPerSecond = parseRedisUint(values["instantaneous_ops_per_sec"])
	result.KeyspaceHits = parseRedisUint(values["keyspace_hits"])
	result.KeyspaceMisses = parseRedisUint(values["keyspace_misses"])
	result.EvictedKeys = parseRedisUint(values["evicted_keys"])
	result.FragmentationRatio, _ = strconv.ParseFloat(values["mem_fragmentation_ratio"], 64)
	if total := result.KeyspaceHits + result.KeyspaceMisses; total > 0 {
		result.HitRatePercent = float64(result.KeyspaceHits) / float64(total) * 100
	}
	return result
}

func (p *Platform) monitoringTasks() MonitoringTasks {
	result := MonitoringTasks{Status: "unavailable", Queues: make([]MonitoringQueue, 0, len(AllQueueNames))}
	if p == nil || p.Redis == nil {
		return result
	}
	result.Status = "healthy"
	result.WorkersReady = p.WorkersReady()
	if !result.WorkersReady {
		result.Status = "degraded"
	}
	if p.BackgroundLoad != nil {
		stats := p.BackgroundLoad.Snapshot()
		result.BackgroundWorkers = MonitoringBackgroundWorkers{
			Limit:          stats.Limit,
			Active:         stats.Active,
			Maximum:        stats.Maximum,
			CPUPercent:     stats.CPUPercent,
			MemoryPercent:  stats.MemoryPercent,
			MetricsHealthy: stats.CPUValid && stats.MemoryValid,
		}
	}
	inspector := asynq.NewInspectorFromRedisClient(p.Redis)
	existingNames, err := inspector.Queues()
	if err != nil {
		result.Status = "degraded"
		return result
	}
	existing := make(map[string]struct{}, len(existingNames))
	for _, name := range existingNames {
		existing[name] = struct{}{}
	}
	for _, name := range AllQueueNames {
		queue := MonitoringQueue{Name: name, Status: "healthy"}
		if _, ok := existing[name]; !ok {
			result.Queues = append(result.Queues, queue)
			continue
		}
		info, err := inspector.GetQueueInfo(name)
		if err != nil {
			if errors.Is(err, asynq.ErrQueueNotFound) {
				result.Queues = append(result.Queues, queue)
				continue
			}
			queue.Status = "unavailable"
			result.Status = "degraded"
			result.Queues = append(result.Queues, queue)
			continue
		}
		queue.Paused = info.Paused
		queue.Pending = info.Pending
		queue.Active = info.Active
		queue.Scheduled = info.Scheduled
		queue.Retry = info.Retry
		queue.Archived = info.Archived
		queue.ProcessedToday = info.Processed
		queue.FailedToday = info.Failed
		queue.ProcessedTotal = info.ProcessedTotal
		queue.FailedTotal = info.FailedTotal
		queue.LatencySeconds = info.Latency.Seconds()
		queue.MemoryUsageBytes = info.MemoryUsage
		result.Queues = append(result.Queues, queue)
	}
	return result
}

func monitoringApplication() MonitoringApplication {
	result := MonitoringApplication{Status: "healthy", Series: []MonitoringMetric{}}
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		result.Status = "degraded"
	}
	for _, family := range families {
		name := family.GetName()
		if !strings.HasPrefix(name, "remail_") {
			continue
		}
		for _, metric := range family.GetMetric() {
			series := MonitoringMetric{
				Name:   name,
				Help:   family.GetHelp(),
				Type:   strings.ToLower(family.GetType().String()),
				Labels: make(map[string]string, len(metric.GetLabel())),
			}
			for _, label := range metric.GetLabel() {
				series.Labels[label.GetName()] = label.GetValue()
			}
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				series.Value = metric.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				series.Value = metric.GetGauge().GetValue()
			case dto.MetricType_UNTYPED:
				series.Value = metric.GetUntyped().GetValue()
			case dto.MetricType_HISTOGRAM:
				histogram := metric.GetHistogram()
				series.Count = histogram.GetSampleCount()
				series.Sum = histogram.GetSampleSum()
				if series.Count > 0 {
					series.Average = series.Sum / float64(series.Count)
					series.P50 = histogramQuantile(0.50, series.Count, histogram.GetBucket())
					series.P95 = histogramQuantile(0.95, series.Count, histogram.GetBucket())
				}
			default:
				continue
			}
			result.Series = append(result.Series, series)
		}
	}
	sort.Slice(result.Series, func(i, j int) bool {
		if result.Series[i].Name != result.Series[j].Name {
			return result.Series[i].Name < result.Series[j].Name
		}
		return metricLabelString(result.Series[i].Labels) < metricLabelString(result.Series[j].Labels)
	})
	return result
}

func parseRedisInfo(info string) map[string]string {
	values := make(map[string]string)
	for line := range strings.SplitSeq(info, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	return values
}

func parseRedisUint(value string) uint64 {
	parsed, _ := strconv.ParseUint(value, 10, 64)
	return parsed
}

func histogramQuantile(quantile float64, count uint64, buckets []*dto.Bucket) float64 {
	if count == 0 || len(buckets) == 0 {
		return 0
	}
	target := quantile * float64(count)
	previousCount := uint64(0)
	previousUpper := float64(0)
	for _, bucket := range buckets {
		currentCount := bucket.GetCumulativeCount()
		upper := bucket.GetUpperBound()
		if float64(currentCount) >= target {
			if math.IsInf(upper, 1) {
				return previousUpper
			}
			if currentCount == previousCount {
				return upper
			}
			fraction := (target - float64(previousCount)) / float64(currentCount-previousCount)
			return previousUpper + (upper-previousUpper)*fraction
		}
		previousCount = currentCount
		if !math.IsInf(upper, 1) {
			previousUpper = upper
		}
	}
	return previousUpper
}

func metricLabelString(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var value strings.Builder
	for _, key := range keys {
		value.WriteString(key)
		value.WriteByte('=')
		value.WriteString(labels[key])
		value.WriteByte(',')
	}
	return value.String()
}
