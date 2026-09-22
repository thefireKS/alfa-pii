package loadgen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServiceMetrics is a point-in-time snapshot of the service's Prometheus
// metrics relevant to a load run.
type ServiceMetrics struct {
	RequestsTotal map[string]int64 // key: operation|outcome
	StoreRecords  int64
	StoreBytes    int64
	StoreTTL      int64
	StoreFailures map[string]int64
	Active        int64
}

// MetricsPoller polls the service /metrics endpoint and, when a container name
// is given, docker stats for CPU/RSS. It is safe for concurrent use.
type MetricsPoller struct {
	baseURL       string
	containerName string
	client        *http.Client

	mu          sync.Mutex
	last        ServiceMetrics
	peakCPU     float64
	peakRSS     int64
	lastCPU     float64
	lastRSS     int64
	storeSamples []storeSample
}

type storeSample struct {
	at      time.Time
	records int64
	bytes   int64
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
	p.storeSamples = append(p.storeSamples, storeSample{at: time.Now(), records: sm.StoreRecords, bytes: sm.StoreBytes})
	p.mu.Unlock()
}

// pollMetrics fetches and parses the /metrics endpoint.
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
	return sm
}

var (
	reRequests = regexp.MustCompile(`pii_requests_total\{operation="([^"]+)",outcome="([^"]+)"\} (\d+)`)
	reRecords  = regexp.MustCompile(`pii_store_records (\d+)`)
	reBytes    = regexp.MustCompile(`pii_store_bytes ([0-9.eE+]+)`)
	reTTL      = regexp.MustCompile(`pii_store_ttl_expired_total (\d+)`)
	reFail     = regexp.MustCompile(`pii_store_failures_total\{reason="([^"]+)"\} (\d+)`)
	reActive   = regexp.MustCompile(`pii_active_requests (\d+)`)
)

func parseMetrics(body string, sm *ServiceMetrics) {
	for _, m := range reRequests.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.ParseInt(m[3], 10, 64)
		sm.RequestsTotal[m[1]+"|"+m[2]] = n
	}
	if m := reRecords.FindStringSubmatch(body); m != nil {
		sm.StoreRecords, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m := reBytes.FindStringSubmatch(body); m != nil {
		f, _ := strconv.ParseFloat(m[1], 64)
		sm.StoreBytes = int64(f)
	}
	if m := reTTL.FindStringSubmatch(body); m != nil {
		sm.StoreTTL, _ = strconv.ParseInt(m[1], 10, 64)
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
func (p *MetricsPoller) StoreDynamics() []storeSample {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]storeSample, len(p.storeSamples))
	copy(out, p.storeSamples)
	return out
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