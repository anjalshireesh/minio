package cmd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio/internal/logger"
	"github.com/minio/minio/internal/mcontext"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"github.com/shirou/gopsutil/v3/host"
)

const (
	resourceMetricsCollectionInterval = time.Minute
	resourceMetricsCacheInterval      = time.Minute

	// drive stats
	totalInodes   MetricName = "total_inodes"
	readsPerSec   MetricName = "reads_per_sec"
	writesPerSec  MetricName = "writes_per_sec"
	readKBPerSec  MetricName = "read_kb_per_sec"
	writeKBPerSec MetricName = "write_kb_per_sec"
	readsAwait    MetricName = "reads_await"
	writesAwait   MetricName = "writes_await"
	percUtil      MetricName = "perc_util"
	usedInodes    MetricName = "used_inodes"

	// network stats
	interfaceRxBytes  MetricName = "if_rx_bytes"
	interfaceRxErrors MetricName = "if_rx_errors"
	interfaceTxBytes  MetricName = "if_tx_bytes"
	interfaceTxErrors MetricName = "if_tx_errors"

	// memory stats
	used      MetricName = "used"
	free      MetricName = "free"
	shared    MetricName = "shared"
	buffers   MetricName = "buffers"
	cached    MetricName = "cached"
	available MetricName = "available"

	// cpu stats
	cpuUser   MetricName = "user"
	cpuSystem MetricName = "system"
	cpuIOWait MetricName = "iowait"
	cpuIdle   MetricName = "idle"
	cpuLoad1  MetricName = "load1"
	cpuLoad5  MetricName = "load5"
	cpuLoad15 MetricName = "load15"
)

var (
	resourceCollector *minioResourceCollector
	// resourceMetricsMap is a map of subsystem to its metrics
	resourceMetricsMap map[MetricSubsystem]ResourceMetrics
	// resourceMetricsHelpMap maps metric name to its help string
	resourceMetricsHelpMap map[MetricName]string
)

// PeerResourceMetrics represents the resource metrics
// retrieved from a peer, along with errors if any
type PeerResourceMetrics struct {
	Metrics map[MetricSubsystem]ResourceMetrics
	Errors  []string
}

// ResourceMetrics is a map of unique key identifying
// a resource metric (e.g. reads_per_sec_{node}_{drive})
// to its data
type ResourceMetrics map[string]ResourceMetric

// ResourceMetric represents a single resource metric
// The metrics are collected from all servers periodically
// and stored in the resource metrics map.
// It also maintains the count of number of times this metric
// was collected since the server started, and the sum,
// average and max values across the same.
type ResourceMetric struct {
	Name   MetricName
	Labels map[string]string

	// value captured in current cycle
	Current float64

	// Used when system provides cumulative (since uptime) values
	// helps in calculating the current value by comparing the new
	// cumulative value with previous one
	Cumulative float64

	Max   float64
	Avg   float64
	Sum   float64
	Count uint64
}

func init() {
	interval := fmt.Sprintf("%ds", int(resourceMetricsCollectionInterval.Seconds()))
	resourceMetricsHelpMap = map[MetricName]string{
		interfaceRxBytes:  "Bytes received on the interface in " + interval,
		interfaceRxErrors: "Receive errors in " + interval,
		interfaceTxBytes:  "Bytes transmitted in " + interval,
		interfaceTxErrors: "Transmit errors in " + interval,
		total:             "Total memory on the node",
		used:              "Used memory on the node",
		free:              "Free memory on the node",
		shared:            "Shared memory on the node",
		buffers:           "Buffers memory on the node",
		cached:            "Cached memory on the node",
		available:         "Available memory on the node",
		readsPerSec:       "Reads per second on a drive",
		writesPerSec:      "Writes per second on a drive",
		readKBPerSec:      "Kilobytes read per second on a drive",
		writeKBPerSec:     "Kilobytes written per second on a drive",
		readsAwait:        "Average time for read requests to be served on a drive",
		writesAwait:       "Average time for write requests to be served on a drive",
		percUtil:          "Percentage of time the disk was busy since uptime",
		usedBytes:         "Used bytes on a drive",
		totalBytes:        "Total bytes on a drive",
		usedInodes:        "Total inodes used on a drive",
		totalInodes:       "Total inodes on a drive",
		cpuUser:           "CPU user time",
		cpuSystem:         "CPU system time",
		cpuIdle:           "CPU idle time",
		cpuIOWait:         "CPU ioWait time",
		cpuLoad1:          "CPU load average 1min",
		cpuLoad5:          "CPU load average 5min",
		cpuLoad15:         "CPU load average 15min",
	}
	resourceMetricsGroups := []*MetricsGroup{
		getResourceMetrics(),
	}

	resourceCollector = newMinioResourceCollector(resourceMetricsGroups)
	go startResourceMetricsCollection()
}

func updateResourceMetrics(subSys MetricSubsystem, name MetricName, val float64, labels map[string]string, isCumulative bool) {
	subsysMetrics, found := resourceMetricsMap[subSys]
	if !found {
		subsysMetrics = ResourceMetrics{}
	}

	// labels are used to uniquely identify a metric
	// e.g. readsPerSec -> node + drive
	sfx := ""
	for _, v := range labels {
		if len(sfx) > 0 {
			sfx += "_"
		}
		sfx += v
	}

	key := string(name) + "_" + sfx
	metric, found := subsysMetrics[key]
	if !found {
		metric = ResourceMetric{
			Name:   name,
			Labels: labels,
		}
	}

	if isCumulative {
		metric.Current = val - metric.Cumulative
		metric.Cumulative = val
	} else {
		metric.Current = val
	}

	if metric.Current > metric.Max {
		metric.Max = val
	}

	metric.Sum += metric.Current
	metric.Count++

	metric.Avg = metric.Sum / float64(metric.Count)
	subsysMetrics[key] = metric

	resourceMetricsMap[subSys] = subsysMetrics
}

func collectDriveMetrics(m madmin.RealtimeMetrics) {
	upt, _ := host.Uptime()
	kib := 1 << 10
	sectorSize := uint64(512)

	node := globalMinioAddr
	// these are only local metrics; so pick node from the first host
	for h := range m.ByHost {
		node = h
		break
	}

	for d, dm := range m.ByDisk {
		stats := dm.IOStats
		labels := map[string]string{"drive": d, "node": node}
		updateResourceMetrics(driveSubsystem, readsPerSec, float64(stats.ReadIOs)/float64(upt), labels, false)

		readBytes := stats.ReadSectors * sectorSize
		readKib := float64(readBytes) / float64(kib)
		readKibPerSec := readKib / float64(upt)
		updateResourceMetrics(driveSubsystem, readKBPerSec, readKibPerSec, labels, false)

		updateResourceMetrics(driveSubsystem, writesPerSec, float64(stats.WriteIOs)/float64(upt), labels, false)

		writeBytes := stats.WriteSectors * sectorSize
		writeKib := float64(writeBytes) / float64(kib)
		writeKibPerSec := writeKib / float64(upt)
		updateResourceMetrics(driveSubsystem, writeKBPerSec, writeKibPerSec, labels, false)

		rdAwait := 0.0
		if stats.ReadIOs > 0 {
			rdAwait = float64(stats.ReadTicks) / float64(stats.ReadIOs)
		}
		updateResourceMetrics(driveSubsystem, readsAwait, rdAwait, labels, false)

		wrAwait := 0.0
		if stats.WriteIOs > 0 {
			wrAwait = float64(stats.WriteTicks) / float64(stats.WriteIOs)
		}
		updateResourceMetrics(driveSubsystem, writesAwait, wrAwait, labels, false)

		updateResourceMetrics(driveSubsystem, percUtil, float64(stats.TotalTicks)/float64(upt*10), labels, false)
	}

	objLayer := newObjectLayerFn()
	// Service not initialized yet
	if objLayer == nil {
		return
	}
	storageInfo := objLayer.StorageInfo(GlobalContext)

	for _, disk := range storageInfo.Disks {
		labels := map[string]string{"drive": disk.Endpoint, "node": node}
		updateResourceMetrics(driveSubsystem, usedBytes, float64(disk.UsedSpace), labels, false)
		updateResourceMetrics(driveSubsystem, totalBytes, float64(disk.TotalSpace), labels, false)
		updateResourceMetrics(driveSubsystem, usedInodes, float64(disk.UsedInodes), labels, false)
		updateResourceMetrics(driveSubsystem, totalInodes, float64(disk.FreeInodes+disk.UsedInodes), labels, false)
	}
}

func collectLocalResourceMetrics() {
	var types madmin.MetricType = madmin.MetricsDisk | madmin.MetricNet | madmin.MetricsMem | madmin.MetricsCPU

	hostsMap := map[string]struct{}{}
	hostsMap[globalLocalNodeName] = struct{}{}
	m := collectLocalMetrics(types, collectMetricsOpts{
		hosts: hostsMap,
	})

	for host, hm := range m.ByHost {
		if len(host) > 0 {
			labels := map[string]string{"node": host}
			if hm.Net != nil && len(hm.Net.NetStats.Name) > 0 {
				stats := hm.Net.NetStats
				labels["interface"] = stats.Name
				updateResourceMetrics(netSubsystem, interfaceRxBytes, float64(stats.RxBytes), labels, true)
				updateResourceMetrics(netSubsystem, interfaceRxErrors, float64(stats.RxErrors), labels, true)
				updateResourceMetrics(netSubsystem, interfaceTxBytes, float64(stats.TxBytes), labels, true)
				updateResourceMetrics(netSubsystem, interfaceTxErrors, float64(stats.TxErrors), labels, true)
			}
			if hm.Mem != nil && len(hm.Mem.Info.Addr) > 0 {
				stats := hm.Mem.Info
				updateResourceMetrics(memSubsystem, total, float64(stats.Total), labels, false)
				updateResourceMetrics(memSubsystem, used, float64(stats.Used), labels, false)
				updateResourceMetrics(memSubsystem, free, float64(stats.Free), labels, false)
				updateResourceMetrics(memSubsystem, shared, float64(stats.Shared), labels, false)
				updateResourceMetrics(memSubsystem, buffers, float64(stats.Buffers), labels, false)
				updateResourceMetrics(memSubsystem, available, float64(stats.Available), labels, false)
				updateResourceMetrics(memSubsystem, cached, float64(stats.Cache), labels, false)
			}
			if hm.CPU != nil {
				ts := hm.CPU.TimesStat
				if ts != nil {
					tot := ts.User + ts.System + ts.Idle + ts.Iowait
					cpuUserVal := math.Round(float64(ts.User)/float64(tot)*100*100) / 100
					updateResourceMetrics(cpuSubsystem, cpuUser, cpuUserVal, labels, false)
					cpuSystemVal := math.Round(float64(ts.System)/float64(tot)*100*100) / 100
					updateResourceMetrics(cpuSubsystem, cpuSystem, cpuSystemVal, labels, false)
					cpuIdleVal := math.Round(float64(ts.Idle)/float64(tot)*100*100) / 100
					updateResourceMetrics(cpuSubsystem, cpuIdle, cpuIdleVal, labels, false)
					cpuIOWaitVal := math.Round(float64(ts.Iowait)/float64(tot)*100*100) / 100
					updateResourceMetrics(cpuSubsystem, cpuIOWait, cpuIOWaitVal, labels, false)
				}
				ls := hm.CPU.LoadStat
				if ls != nil {
					updateResourceMetrics(cpuSubsystem, cpuLoad1, ls.Load1, labels, false)
					updateResourceMetrics(cpuSubsystem, cpuLoad5, ls.Load5, labels, false)
					updateResourceMetrics(cpuSubsystem, cpuLoad15, ls.Load15, labels, false)
				}
			}
			break // only one host expected
		}
	}

	collectDriveMetrics(m)
}

func collectRemoteResourceMetrics(ctx context.Context) []PeerResourceMetrics {
	m := []PeerResourceMetrics{}

	if !globalIsDistErasure {
		return m
	}

	return globalNotificationSys.GetResourceMetrics(ctx)
}

// startResourceMetricsCollection - starts the job for collecting resource metrics
func startResourceMetricsCollection() {
	resourceMetricsMap = make(map[MetricSubsystem]ResourceMetrics)
	metricsTimer := time.NewTimer(resourceMetricsCollectionInterval)

	collectLocalResourceMetrics()

	defer metricsTimer.Stop()

	for {
		select {
		case <-GlobalContext.Done():
			return
		case <-metricsTimer.C:
			collectLocalResourceMetrics()

			// Reset the timer for next cycle.
			metricsTimer.Reset(resourceMetricsCollectionInterval)
		}
	}
}

// minioResourceCollector is the Collector for resource metrics
type minioResourceCollector struct {
	metricsGroups []*MetricsGroup
	desc          *prometheus.Desc
}

// Describe sends the super-set of all possible descriptors of metrics
func (c *minioResourceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect is called by the Prometheus registry when collecting metrics.
func (c *minioResourceCollector) Collect(ch chan<- prometheus.Metric) {
	// Expose MinIO's version information
	minioVersionInfo.WithLabelValues(Version, CommitID).Set(1.0)

	populateAndPublish(c.metricsGroups, func(metric Metric) bool {
		labels, values := getOrderedLabelValueArrays(metric.VariableLabels)
		values = append(values, globalLocalNodeName)
		labels = append(labels, serverName)

		if metric.Description.Type == histogramMetric {
			if metric.Histogram == nil {
				return true
			}
			for k, v := range metric.Histogram {
				labels = append(labels, metric.HistogramBucketLabel)
				values = append(values, k)
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(
						prometheus.BuildFQName(string(metric.Description.Namespace),
							string(metric.Description.Subsystem),
							string(metric.Description.Name)),
						metric.Description.Help,
						labels,
						metric.StaticLabels,
					),
					prometheus.GaugeValue,
					float64(v),
					values...)
			}
			return true
		}

		metricType := prometheus.GaugeValue
		if metric.Description.Type == counterMetric {
			metricType = prometheus.CounterValue
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				prometheus.BuildFQName(string(metric.Description.Namespace),
					string(metric.Description.Subsystem),
					string(metric.Description.Name)),
				metric.Description.Help,
				labels,
				metric.StaticLabels,
			),
			metricType,
			metric.Value,
			values...)
		return true
	})
}

// newMinioResourceCollector describes the collector
// and returns reference of minio resource Collector
// It creates the Prometheus Description which is used
// to define Metric and  help string
func newMinioResourceCollector(metricsGroups []*MetricsGroup) *minioResourceCollector {
	return &minioResourceCollector{
		metricsGroups: metricsGroups,
		desc:          prometheus.NewDesc("minio_resource_stats", "Resource statistics exposed by MinIO server", nil, nil),
	}
}

func getNodeCPUStatSystem() MetricDescription {
	return MetricDescription{
		Namespace: nodeMetricNamespace,
		Subsystem: cpuSubsystem,
		Name:      cpuSystem,
		Help:      "CPU system time",
		Type:      gaugeMetric,
	}
}

func prepareResourceMetrics(rm ResourceMetric, subSys MetricSubsystem) []Metric {
	help := resourceMetricsHelpMap[rm.Name]
	name := rm.Name

	metrics := []Metric{}
	metrics = append(metrics, Metric{
		Description:    getNodeMetricDescription(subSys, name, help),
		Value:          rm.Current,
		VariableLabels: rm.Labels,
	})

	avgName := MetricName(fmt.Sprintf("%s_avg", name))
	avgHelp := fmt.Sprintf("%s (avg)", help)
	metrics = append(metrics, Metric{
		Description:    getNodeMetricDescription(subSys, avgName, avgHelp),
		Value:          math.Round(rm.Avg*100) / 100,
		VariableLabels: rm.Labels,
	})

	maxName := MetricName(fmt.Sprintf("%s_max", name))
	maxHelp := fmt.Sprintf("%s (max)", help)
	metrics = append(metrics, Metric{
		Description:    getNodeMetricDescription(subSys, maxName, maxHelp),
		Value:          rm.Max,
		VariableLabels: rm.Labels,
	})

	return metrics
}

func getNodeMetricDescription(subSys MetricSubsystem, name MetricName, help string) MetricDescription {
	return MetricDescription{
		Namespace: nodeMetricNamespace,
		Subsystem: subSys,
		Name:      name,
		Help:      help,
		Type:      gaugeMetric,
	}
}

func getResourceMetricsFromCache(metricsCache map[MetricSubsystem]ResourceMetrics) (metrics []Metric) {
	metrics = make([]Metric, 0, 50)

	subSystems := []MetricSubsystem{netSubsystem, memSubsystem, driveSubsystem, cpuSubsystem}
	for _, subSys := range subSystems {
		stats, found := metricsCache[subSys]
		if found {
			for _, m := range stats {
				metrics = append(metrics, prepareResourceMetrics(m, subSys)...)
			}
		}
	}

	return metrics
}

func getResourceMetrics() *MetricsGroup {
	mg := &MetricsGroup{
		cacheInterval: resourceMetricsCacheInterval,
	}
	mg.RegisterRead(func(ctx context.Context) (metrics []Metric) {
		metrics = make([]Metric, 0, 50)
		metrics = append(metrics, getResourceMetricsFromCache(resourceMetricsMap)...)

		cctx, cancel := context.WithTimeout(ctx, time.Second/2)
		remoteMetrics := collectRemoteResourceMetrics(cctx)
		cancel()

		for _, rm := range remoteMetrics {
			if len(rm.Errors) == 0 {
				metrics = append(metrics, getResourceMetricsFromCache(rm.Metrics)...)
			} else {
				err := errors.New("error fetching metrics from remote server: " + strings.Join(rm.Errors, ","))
				logger.LogIf(ctx, err)
			}
		}
		return
	})
	return mg
}

// metricsHandler is the prometheus handler for resource metrics
func metricsResourceHandler() http.Handler {
	registry := prometheus.NewRegistry()

	logger.CriticalIf(GlobalContext, registry.Register(resourceCollector))
	gatherers := prometheus.Gatherers{
		registry,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, ok := r.Context().Value(mcontext.ContextTraceKey).(*mcontext.TraceCtxt)
		if ok {
			tc.FuncName = "handler.MetricsResource"
			tc.ResponseRecorder.LogErrBody = true
		}

		mfs, err := gatherers.Gather()
		if err != nil {
			if len(mfs) == 0 {
				writeErrorResponseJSON(r.Context(), w, toAdminAPIErr(r.Context(), err), r.URL)
				return
			}
		}

		contentType := expfmt.Negotiate(r.Header)
		w.Header().Set("Content-Type", string(contentType))

		enc := expfmt.NewEncoder(w, contentType)
		for _, mf := range mfs {
			if err := enc.Encode(mf); err != nil {
				logger.LogIf(r.Context(), err)
				return
			}
		}
		if closer, ok := enc.(expfmt.Closer); ok {
			closer.Close()
		}
	})
}
