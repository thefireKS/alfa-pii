// Package metrics exposes service observability over HTTP in the Prometheus
// text format so it can be polled locally. Collectors are registered on a
// private registry, never the global default, keeping state explicit and
// testable. Labels are drawn from a small fixed set: operation, outcome and
// error class. No payload text, found values, payload_id or consumer names
// from the request body are ever used as labels.
package metrics

import (
	"net/http"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Operations are the three request entry points.
const (
	OperationProcess  = "process"
	OperationMask     = "mask"
	OperationRestore  = "restore"
)

// Outcomes classify the result of a request. The set is fixed and bounded so
// the number of label series never grows with input data.
const (
	OutcomeMask          = "mask"          // a new mask was created
	OutcomeRestore       = "restore"       // a restoration was performed
	OutcomeRepeat        = "repeat"        // a stored result was returned for a repeat
	OutcomeConflict      = "conflict"      // 409
	OutcomeError         = "error"         // 500 internal error
	OutcomeOverload      = "overload"      // 429 admission or capacity
	OutcomeCancel        = "cancel"        // request context cancelled
	OutcomeInvalid       = "invalid"       // 400
	OutcomeTooLarge      = "too_large"     // 413
	OutcomeUnauthorized  = "unauthorized"  // 401
	OutcomeForbidden     = "forbidden"     // 403
	OutcomeNotFound      = "not_found"     // 404
	OutcomeMethodNotAllowed = "method_not_allowed" // 405
)

// Duration classes separate successful responses from fast overload refusals
// and errors so percentiles are not skewed by cheap 429s.
const (
	ClassSuccess  = "success"
	ClassOverload = "overload"
	ClassError    = "error"
	ClassOther    = "other"
)

// TokenMethod names the token estimation heuristic. There is no exact
// tokenizer configured, so the token metric is explicitly labelled as an
// estimate and is never presented as an exact token count.
const TokenMethod = "estimate_runes_div4"

// Store failure reasons reported by the storage layer.
const (
	StoreFailCapacity = "capacity"
	StoreFailBusy     = "busy"
)

// Store areas are the fixed storage scopes. The unauthenticated /process scope
// and the managed consumers use separate store instances, so their capacity and
// phase accounting are isolated. The area is a fixed label, never derived from
// request data.
const (
	AreaProcess = "process"
	AreaManaged = "managed"
)

// Metrics holds the service's Prometheus collectors.
type Metrics struct {
	registry *prometheus.Registry

	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	active   prometheus.Gauge

	textBytes *prometheus.CounterVec
	textChars *prometheus.CounterVec
	tokens    *prometheus.CounterVec

	storeRecords *prometheus.GaugeVec
	storeBytes   *prometheus.GaugeVec
	storeTTL     *prometheus.CounterVec
	storeFail    *prometheus.CounterVec
}

// New builds a Metrics with all collectors registered on a private registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_requests_total",
			Help: "Total requests by operation and outcome.",
		}, []string{"operation", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pii_response_duration_seconds",
			Help:    "Service response duration by operation and class, measured until the response is written (including serialization), enabling mean and p50/p95/p99.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms .. ~16s
		}, []string{"operation", "class"}),
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pii_active_requests",
			Help: "Currently in-flight requests.",
		}),
		textBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_text_bytes_total",
			Help: "Total payload bytes processed by operation.",
		}, []string{"operation"}),
		textChars: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_text_chars_total",
			Help: "Total payload Unicode code points processed by operation.",
		}, []string{"operation"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_tokens_total",
			Help: "Estimated tokens processed by operation and counting method.",
		}, []string{"operation", "method"}),
		storeRecords: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pii_store_records",
			Help: "Current number of stored correspondences by area and phase.",
		}, []string{"area", "phase"}),
		storeBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pii_store_bytes",
			Help: "Current accounted bytes held by stored correspondences by area and phase.",
		}, []string{"area", "phase"}),
		storeTTL: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_store_ttl_expired_total",
			Help: "Total correspondences evicted because their TTL elapsed, by area and phase.",
		}, []string{"area", "phase"}),
		storeFail: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_store_failures_total",
			Help: "Total storage failures by reason.",
		}, []string{"reason"}),
	}
	reg.MustRegister(
		m.requests, m.duration, m.active,
		m.textBytes, m.textChars, m.tokens,
		m.storeRecords, m.storeBytes, m.storeTTL, m.storeFail,
	)
	return m
}

// Handler returns an HTTP handler that serves the metrics in the Prometheus
// text format. It never returns user data.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveRequest records a completed request with its outcome and duration.
func (m *Metrics) ObserveRequest(operation, outcome, class string, seconds float64) {
	m.requests.WithLabelValues(operation, outcome).Inc()
	m.duration.WithLabelValues(operation, class).Observe(seconds)
}

// ObserveOverload records an admission refusal that never reached the
// operation. It is counted as an overload outcome and a fast overload duration.
func (m *Metrics) ObserveOverload(operation string, seconds float64) {
	m.requests.WithLabelValues(operation, OutcomeOverload).Inc()
	m.duration.WithLabelValues(operation, ClassOverload).Observe(seconds)
}

// ObserveText records the volume of a processed payload: bytes, code points
// and the estimated token count with the counting method named explicitly.
func (m *Metrics) ObserveText(operation, text string) {
	bytes := uint64(len(text))
	chars := uint64(utf8.RuneCountInString(text))
	m.textBytes.WithLabelValues(operation).Add(float64(bytes))
	m.textChars.WithLabelValues(operation).Add(float64(chars))
	m.tokens.WithLabelValues(operation, TokenMethod).Add(float64(EstimateTokens(text)))
}

// ActiveInc and ActiveDec track the number of in-flight requests.
func (m *Metrics) ActiveInc() { m.active.Inc() }
func (m *Metrics) ActiveDec() { m.active.Dec() }

// StoreRecordAdded and StoreRecordRemoved track the current record count by
// area and phase.
func (m *Metrics) StoreRecordAdded(area, phase string)   { m.storeRecords.WithLabelValues(area, phase).Inc() }
func (m *Metrics) StoreRecordRemoved(area, phase string) { m.storeRecords.WithLabelValues(area, phase).Dec() }

// StoreBytesDelta adjusts the accounted store bytes by area and phase.
func (m *Metrics) StoreBytesDelta(area, phase string, delta int64) {
	m.storeBytes.WithLabelValues(area, phase).Add(float64(delta))
}

// StoreTTLExpired records a TTL eviction by area and phase.
func (m *Metrics) StoreTTLExpired(area, phase string) {
	m.storeTTL.WithLabelValues(area, phase).Inc()
}

// StoreFailure records a storage failure by reason.
func (m *Metrics) StoreFailure(reason string) { m.storeFail.WithLabelValues(reason).Inc() }

// EstimateTokens approximates the number of tokens in text. There is no exact
// tokenizer configured, so this is a documented heuristic: roughly one token
// per four Unicode code points, a common approximation for mixed-language
// text. It is never presented as an exact token count; the metric label names
// the method.
func EstimateTokens(text string) uint64 {
	return uint64((utf8.RuneCountInString(text) + 3) / 4)
}