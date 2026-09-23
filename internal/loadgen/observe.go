package loadgen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServiceMetrics is a point-in-time snapshot of the service's Prometheus
// metrics relevant to a load run.
type ServiceMetrics struct {
	RequestsTotal map[string]int64 // key: operation|outcome
	// Store records and bytes broken down by phase (pending/replay).
	StoreRecordsPending int64
	StoreRecordsReplay  int64
	StoreBytesPending   int64
	StoreBytesReplay    int64
	StoreTTL            int64
	StoreFailures       map[string]int64
	Active              int64
	// ok reports whether the metrics were successfully fetched. When false the
	// other fields are not meaningful and should be reported as missing data,
	// not as zero values.
	ok bool
}

// OK reports whether the metrics were successfully fetched.
func (s ServiceMetrics) OK() bool { return s.ok }

// MetricsPoller polls the service /metrics endpoint and, when a container name
// is given, docker stats for CPU/RSS. It is safe for concurrent use.
type MetricsPoller struct {
	baseURL       string
	containerName string
	client        *http.Client

	mu           sync.Mutex
	last         ServiceMetrics
	peakCPU      float64
	peakRSS      int64
	lastCPU      float64
	lastRSS      int64
	storeSamples []StoreSample
}

// StoreSample is one point in the store dynamics time series. It captures the
// store fill (records/bytes) and the cumulative request counters (mask,
// restore, overload, error) at the poll time, so the report can show filling,
// freeing, successful mask/restore, 429 and errors over time.
type StoreSample struct {
	At       time.Time
	Records  int64
	Bytes    int64
	Mask     int64
	Restore  int64
	Overload int64
	Errors   int64
	// OK reports whether the metrics fetch succeeded for this sample.
	OK bool
}

// NewMetricsPoller builds a poller for the given base URL and optional
// container name.
func NewMetricsPoller(baseURL, containerName string) *MetricsPoller {
	return &MetricsPoller{
		baseURL:       baseURL,
		containerName: containerName,
		client:        &http.Client{Timeout: 5 * time.Second},
	}
}

// Poll fetches the current metrics and docker stats.
func (p *MetricsPoller) Poll(ctx context.Context) {
	sm := p.pollMetrics(ctx)
	p.mu.Lock()
	p.last = sm
	if p.containerName != "" {
		cpu, rss := p.pollDocker()
		if cpu > 0 {
			p.lastCPU = cpu
			if cpu > p.peakCPU {
				p.peakCPU = cpu
			}
		}
		if rss > 0 {
			p.lastRSS = rss
			if rss > p.peakRSS {
				p.peakRSS = rss
			}
		}
	}
	p.storeSamples = append(p.storeSamples, StoreSample{
		At:       time.Now(),
		Records:  sm.StoreRecordsPending + sm.StoreRecordsReplay,
		Bytes:    sm.StoreBytesPending + sm.StoreBytesReplay,
		Mask:     sm.RequestsTotal["process|mask"],
		Restore:  sm.RequestsTotal["process|restore"],
		Overload: sm.RequestsTotal["process|overload"],
		Errors:   sm.RequestsTotal["process|error"] + sm.RequestsTotal["process|timeout"],
		OK:       sm.ok,
	})
	p.mu.Unlock()
}

// pollMetrics fetches and parses the /metrics endpoint. On failure it returns a
// ServiceMetrics with ok=false so the caller can distinguish missing data from
// a real zero.
func (p *MetricsPoller) pollMetrics(ctx context.Context) ServiceMetrics {
	sm := ServiceMetrics{
		RequestsTotal: make(map[string]int64),
		StoreFailures: make(map[string]int64),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/metrics", nil)
	if err != nil {
		return sm
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return sm
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return sm
	}
	parseMetrics(string(body), &sm)
	sm.ok = true
	return sm
}

var (
	reRequests = regexp.MustCompile(`pii_requests_total\{operation="([^"]+)",outcome="([^"]+)"\} (\d+)`)
	reRecords  = regexp.MustCompile(`pii_store_records\{area="[^"]+",phase="([^"]+)"\} (\d+)`)
	reBytes    = regexp.MustCompile(`pii_store_bytes\{area="[^"]+",phase="([^"]+)"\} ([0-9.eE+]+)`)
	reTTL      = regexp.MustCompile(`pii_store_ttl_expired_total\{area="[^"]+",phase="[^"]+"\} (\d+)`)
	reFail     = regexp.MustCompile(`pii_store_failures_total\{reason="([^"]+)"\} (\d+)`)
	reActive   = regexp.MustCompile(`pii_active_requests (\d+)`)
)

func parseMetrics(body string, sm *ServiceMetrics) {
	for _, m := range reRequests.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.ParseInt(m[3], 10, 64)
		sm.RequestsTotal[m[1]+"|"+m[2]] = n
	}
	// Store metrics are broken down by area and phase; sum across all area
	// combinations but keep the phase breakdown so the report can show both
	// storage phases.
	for _, m := range reRecords.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.ParseInt(m[2], 10, 64)
		switch m[1] {
		case "pending":
			sm.StoreRecordsPending += n
		case "replay":
			sm.StoreRecordsReplay += n
		}
	}
	for _, m := range reBytes.FindAllStringSubmatch(body, -1) {
		f, _ := strconv.ParseFloat(m[2], 64)
		switch m[1] {
		case "pending":
			sm.StoreBytesPending += int64(f)
		case "replay":
			sm.StoreBytesReplay += int64(f)
		}
	}
	for _, m := range reTTL.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		sm.StoreTTL += n
	}
	for _, m := range reFail.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.ParseInt(m[2], 10, 64)
		sm.StoreFailures[m[1]] = n
	}
	if m := reActive.FindStringSubmatch(body); m != nil {
		sm.Active, _ = strconv.ParseInt(m[1], 10, 64)
	}
}

// pollDocker fetches CPU% and RSS from docker stats for the container.
func (p *MetricsPoller) pollDocker() (cpu float64, rss int64) {
	out, err := exec.Command("docker", "stats", "--no-stream", "--format",
		"{{.CPUPerc}}|{{.MemUsage}}", p.containerName).Output()
	if err != nil {
		return 0, 0
	}
	line := strings.TrimSpace(string(out))
	parts := strings.Split(line, "|")
	if len(parts) != 2 {
		return 0, 0
	}
	cpuStr := strings.TrimSuffix(strings.TrimSpace(parts[0]), "%")
	cpu, _ = strconv.ParseFloat(cpuStr, 64)
	memStr := strings.TrimSpace(parts[1])
	// Format: "123.4MiB / 512MiB"
	memPart := strings.Fields(memStr)
	if len(memPart) > 0 {
		rss = parseMem(memPart[0])
	}
	return cpu, rss
}

// parseMem parses a docker memory value like "123.4MiB" or "1.2GiB".
func parseMem(s string) int64 {
	s = strings.TrimSpace(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "KiB"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "KiB")
	case strings.HasSuffix(s, "MiB"):
		mult = 1 << 20
		s = strings.TrimSuffix(s, "MiB")
	case strings.HasSuffix(s, "GiB"):
		mult = 1 << 30
		s = strings.TrimSuffix(s, "GiB")
	case strings.HasSuffix(s, "B"):
		mult = 1
		s = strings.TrimSuffix(s, "B")
	default:
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(mult))
}

// Snapshot returns the latest metrics and peak resource usage.
func (p *MetricsPoller) Snapshot() (ServiceMetrics, float64, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last, p.peakCPU, p.peakRSS
}

// StoreDynamics returns the sampled store records/bytes over time.
func (p *MetricsPoller) StoreDynamics() []StoreSample {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]StoreSample, len(p.storeSamples))
	copy(out, p.storeSamples)
	return out
}

// GeneratorResources samples the load generator's own memory so the report can
// separate generator memory from service memory. It is an approximation using
// the Go runtime heap, not the process RSS.
type GeneratorResources struct {
	// HeapInUse is the current Go heap in use by the generator process.
	HeapInUse int64
	// PeakHeapInUse is the peak Go heap in use observed during the run.
	PeakHeapInUse int64
}

// SampleGeneratorResources returns a snapshot of the generator's own Go heap in
// use.
func SampleGeneratorResources() GeneratorResources {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return GeneratorResources{HeapInUse: int64(ms.HeapInuse)}
}

// ContainerLimits returns the container resource limits via docker inspect.
func ContainerLimits(containerName string) string {
	out, err := exec.Command("docker", "inspect", "--format",
		"{{.HostConfig.NanoCpus}}|{{.HostConfig.Memory}}", containerName).Output()
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) != 2 {
		return ""
	}
	nano, _ := strconv.ParseInt(parts[0], 10, 64)
	mem, _ := strconv.ParseInt(parts[1], 10, 64)
	return fmt.Sprintf("cpus=%s memory=%s", formatCPU(nano), formatBytes(mem))
}

func formatCPU(nano int64) string {
	if nano <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%.1f", float64(nano)/1e9)
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
}