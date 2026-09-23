// Package httpapi exposes the service over HTTP. Handlers parse requests,
// call the application operation and build responses. The transport boundary
// maps application errors to HTTP statuses. Each request gets a technical
// request identifier, separate from the user-supplied payload_id, that is
// carried through structured logs and metrics.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"alfa-hackathon.local/pii/internal/app"
	"alfa-hackathon.local/pii/internal/metrics"
)

// processRequest is the accepted request body for POST /process, /v1/mask and
// /v1/restore.
type processRequest struct {
	Payload   string `json:"payload"`
	PayloadID string `json:"payload_id"`
}

// processResponse is the success body for the process, mask and restore
// endpoints.
type processResponse struct {
	Result string `json:"result"`
}

// errorResponse is the consistent error body. It never contains the original
// text, found values, keys or correspondence contents.
type errorResponse struct {
	Error string `json:"error"`
}

// Service is the application dependency used by the HTTP layer.
type Service interface {
	Process(ctx context.Context, payloadID, payload string) (app.Result, error)
	Mask(ctx context.Context, consumerName, payloadID, payload string) (app.Result, error)
	Restore(ctx context.Context, consumerName, payloadID, masked string) (app.Result, error)
}

// Authenticator maps a Bearer API key to a consumer name. Identity is derived
// from the secret server-side, never from a client-controlled field or header.
type Authenticator struct {
	bySecret map[string]string
}

// NewAuthenticator builds an Authenticator from a secret-to-consumer map.
func NewAuthenticator(bySecret map[string]string) *Authenticator {
	return &Authenticator{bySecret: bySecret}
}

// Authenticate returns the consumer name for the request's Bearer token, or
// false when the token is missing or unknown.
func (a *Authenticator) Authenticate(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", false
	}
	name, ok := a.bySecret[token]
	return name, ok
}

// Handler holds the HTTP dependencies and the readiness state.
type Handler struct {
	svc     Service
	auth    *Authenticator
	ready   func() bool
	active  chan struct{}
	maxBody int64
	working *workingBudget
	logger  *slog.Logger
	metrics *metrics.Metrics
}

// NewHandler builds the HTTP handler with the given dependencies. A nil logger
// falls back to the default logger; a nil metrics builds a fresh collector set
// so the handler is always observable.
func NewHandler(svc Service, auth *Authenticator, ready func() bool, maxActive int, maxBody int64, logger *slog.Logger, m *metrics.Metrics) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = metrics.New()
	}
	return &Handler{
		svc:     svc,
		auth:    auth,
		ready:   ready,
		active:  make(chan struct{}, maxActive),
		maxBody: maxBody,
		working: newWorkingBudget(0),
		logger:  logger,
		metrics: m,
	}
}

// SetWorkingBudget sets the total in-flight working-memory budget in bytes. It
// must be called before the handler serves requests. A non-positive value
// disables the budget.
func (h *Handler) SetWorkingBudget(bytes int64) {
	h.working = newWorkingBudget(bytes)
}

// Routes returns the HTTP mux with all endpoints registered.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/process", h.handleProcess)
	mux.HandleFunc("/v1/mask", h.handleMask)
	mux.HandleFunc("/v1/restore", h.handleRestore)
	mux.HandleFunc("/livez", h.handleLivez)
	mux.HandleFunc("/readyz", h.handleReadyz)
	mux.Handle("/metrics", h.metrics.Handler())
	return mux
}

// handleProcess implements POST /process. It requires no authentication and
// uses the fixed internal scope.
func (h *Handler) handleProcess(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, metrics.OperationProcess, false, func(ctx context.Context, _ string, req processRequest) (app.Result, error) {
		return h.svc.Process(ctx, req.PayloadID, req.Payload)
	})
}

// handleMask implements POST /v1/mask. The consumer is authenticated by its
// Bearer API key and the operation is always a mask.
func (h *Handler) handleMask(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, metrics.OperationMask, true, func(ctx context.Context, consumer string, req processRequest) (app.Result, error) {
		return h.svc.Mask(ctx, consumer, req.PayloadID, req.Payload)
	})
}

// handleRestore implements POST /v1/restore. The consumer is authenticated by
// its Bearer API key and must hold the restore right.
func (h *Handler) handleRestore(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, metrics.OperationRestore, true, func(ctx context.Context, consumer string, req processRequest) (app.Result, error) {
		return h.svc.Restore(ctx, consumer, req.PayloadID, req.Payload)
	})
}

// handle runs the shared request pipeline: request identifier, structured
// logging, admission, authentication, body decoding, the operation and outcome
// recording. The technical request identifier is generated here and is never
// derived from the user-supplied payload_id.
func (h *Handler) handle(w http.ResponseWriter, r *http.Request, operation string, requireAuth bool, fn func(context.Context, string, processRequest) (app.Result, error)) {
	start := time.Now()
	reqID := newRequestID()
	log := h.logger.With("request_id", reqID, "operation", operation)
	ctx := app.WithLogger(r.Context(), log)
	log.Info("stage", "stage", "accept")

	if r.Method != http.MethodPost {
		h.finish(log, operation, metrics.OutcomeMethodNotAllowed, metrics.ClassOther, start)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	release, ok := h.acquire(w, r)
	if !ok {
		h.finishOverload(log, operation, start)
		return
	}
	defer release()
	h.metrics.ActiveInc()
	defer h.metrics.ActiveDec()

	consumer := ""
	if requireAuth {
		var ok bool
		consumer, ok = h.authenticate(w, r)
		if !ok {
			h.finish(log, operation, metrics.OutcomeUnauthorized, metrics.ClassOther, start)
			return
		}
	}

	// Acquire a share of the total working-memory budget before the body is
	// read, based on the declared Content-Length. This bounds the transient
	// memory of many concurrent large payloads, including the body buffers that
	// exist only while the request is being read. When the budget is exhausted
	// the request is refused with 429.
	working := estimateWorkingBytesFromBody(r.ContentLength, h.maxBody)
	if !h.working.acquire(working) {
		h.finishOverload(log, operation, start)
		writeRetryAfter(w, "working memory budget exceeded")
		return
	}
	defer h.working.release(working)

	req, outcome, ok := h.decodeRequest(w, r)
	if !ok {
		h.finish(log, operation, outcome, metrics.ClassOther, start)
		return
	}

	res, err := fn(ctx, consumer, req)
	if err != nil {
		outcome, class := h.outcomeForError(err)
		h.finish(log, operation, outcome, class, start)
		h.writeAppError(w, err)
		return
	}
	h.metrics.ObserveText(operation, req.Payload)
	h.finish(log, operation, string(res.Outcome), metrics.ClassSuccess, start)
	writeJSON(w, http.StatusOK, processResponse{Result: res.Text})
}

// acquire enforces the active-request limit before the body is read so large
// bodies cannot exhaust memory. It writes a 429 response when the limit is
// reached and returns a release function on success.
func (h *Handler) acquire(w http.ResponseWriter, r *http.Request) (func(), bool) {
	select {
	case h.active <- struct{}{}:
		return func() { <-h.active }, true
	default:
		writeRetryAfter(w, "too many requests")
		return nil, false
	}
}

// authenticate resolves the consumer name from the request's Bearer token.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.auth == nil {
		writeError(w, http.StatusUnauthorized, "authentication not configured")
		return "", false
	}
	name, ok := h.auth.Authenticate(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid or missing API key")
		return "", false
	}
	return name, true
}

// decodeRequest parses and validates the shared request body. It writes the
// error response and returns ok=false on failure, together with the outcome
// class to record.
func (h *Handler) decodeRequest(w http.ResponseWriter, r *http.Request) (processRequest, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)

	var req processRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return processRequest{}, metrics.OutcomeTooLarge, false
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return processRequest{}, metrics.OutcomeInvalid, false
	}
	// Reject trailing data so the body is a single JSON object.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must be a single JSON object")
		return processRequest{}, metrics.OutcomeInvalid, false
	}
	if req.PayloadID == "" {
		writeError(w, http.StatusBadRequest, "payload_id must not be empty")
		return processRequest{}, metrics.OutcomeInvalid, false
	}
	return req, "", true
}

// outcomeForError maps an application error to a metrics outcome and duration
// class. Cancellations are classified separately from internal errors.
func (h *Handler) outcomeForError(err error) (string, string) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return metrics.OutcomeCancel, metrics.ClassError
	case errors.Is(err, app.ErrConflict):
		return metrics.OutcomeConflict, metrics.ClassOther
	case errors.Is(err, app.ErrCapacity), errors.Is(err, app.ErrBusy):
		return metrics.OutcomeOverload, metrics.ClassOverload
	case errors.Is(err, app.ErrUnknownConsumer):
		return metrics.OutcomeUnauthorized, metrics.ClassOther
	case errors.Is(err, app.ErrDisabled), errors.Is(err, app.ErrMaskingDisabled), errors.Is(err, app.ErrRestoreForbidden):
		return metrics.OutcomeForbidden, metrics.ClassOther
	case errors.Is(err, app.ErrNotFound):
		return metrics.OutcomeNotFound, metrics.ClassOther
	default:
		return metrics.OutcomeError, metrics.ClassError
	}
}

// finish records the outcome and duration and logs the completion stage.
func (h *Handler) finish(log *slog.Logger, operation, outcome, class string, start time.Time) {
	seconds := time.Since(start).Seconds()
	h.metrics.ObserveRequest(operation, outcome, class, seconds)
	log.Info("stage", "stage", "complete", "outcome", outcome)
}

// finishOverload records an admission refusal that never reached the operation
// and logs the completion stage.
func (h *Handler) finishOverload(log *slog.Logger, operation string, start time.Time) {
	seconds := time.Since(start).Seconds()
	h.metrics.ObserveOverload(operation, seconds)
	log.Info("stage", "stage", "complete", "outcome", metrics.OutcomeOverload)
}

// writeAppError maps application errors to HTTP statuses at the transport
// boundary.
func (h *Handler) writeAppError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrConflict):
		writeError(w, http.StatusConflict, "text conflicts with existing correspondence")
	case errors.Is(err, app.ErrCapacity):
		writeRetryAfter(w, "storage capacity exceeded")
	case errors.Is(err, app.ErrBusy):
		writeRetryAfter(w, "storage busy creating key")
	case errors.Is(err, app.ErrUnknownConsumer):
		writeError(w, http.StatusUnauthorized, "unknown consumer")
	case errors.Is(err, app.ErrDisabled):
		writeError(w, http.StatusForbidden, "consumer disabled")
	case errors.Is(err, app.ErrMaskingDisabled):
		writeError(w, http.StatusForbidden, "masking disabled for consumer")
	case errors.Is(err, app.ErrRestoreForbidden):
		writeError(w, http.StatusForbidden, "restore forbidden for consumer")
	case errors.Is(err, app.ErrNotFound):
		writeError(w, http.StatusNotFound, "correspondence not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// handleLivez reports process liveness. It never returns user data.
func (h *Handler) handleLivez(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports readiness: configuration and required dependencies
// are initialized. It never returns user data.
func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if h.ready != nil && !h.ready() {
		writeError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// reqCounter is a fallback for request identifiers when the entropy source is
// unavailable.
var reqCounter uint64

// newRequestID returns a short random hex identifier for one request. It is
// generated server-side and is independent of the user-supplied payload_id.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("req-%d", atomic.AddUint64(&reqCounter, 1))
}

func writeRetryAfter(w http.ResponseWriter, msg string) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusTooManyRequests, msg)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The response is already committed; nothing more can be done.
		_ = fmt.Errorf("encode response: %w", err)
	}
}

// workingBudget is a weighted semaphore bounding the total estimated working
// memory of in-flight requests. It is safe for concurrent use.
type workingBudget struct {
	mu    sync.Mutex
	total int64
	used  int64
}

// newWorkingBudget returns a budget with the given total. A non-positive total
// disables the budget: acquire always succeeds.
func newWorkingBudget(total int64) *workingBudget {
	return &workingBudget{total: total}
}

// acquire reserves n bytes of the budget. It reports false when the budget is
// exhausted and reserves nothing.
func (b *workingBudget) acquire(n int64) bool {
	if b.total <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.total {
		return false
	}
	b.used += n
	return true
}

// release returns n bytes to the budget. It must be called exactly once for
// every successful acquire.
func (b *workingBudget) release(n int64) {
	if b.total <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
}

// estimateWorkingBytesFromBody approximates the peak transient working memory
// of processing one request from its declared Content-Length: the body buffer
// while it is read, the decoded payload, the masked output and the replacement
// table. The masked output and table can each approach the payload size
// (markers can be longer than short entities), so a conservative multiple of
// the body length is used. For chunked requests (Content-Length -1) the body
// limit is used as the upper bound. The estimate is validated against RSS and
// is never presented as an exact byte count.
func estimateWorkingBytesFromBody(contentLength, maxBody int64) int64 {
	if contentLength <= 0 {
		contentLength = maxBody
	}
	return 4 * contentLength
}