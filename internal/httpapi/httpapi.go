// Package httpapi exposes the service over HTTP. Handlers parse requests,
// call the application operation and build responses. The transport boundary
// maps application errors to HTTP statuses.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"alfa-hackathon.local/pii/internal/app"
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
}

// NewHandler builds the HTTP handler with the given dependencies.
func NewHandler(svc Service, auth *Authenticator, ready func() bool, maxActive int, maxBody int64) *Handler {
	return &Handler{
		svc:     svc,
		auth:    auth,
		ready:   ready,
		active:  make(chan struct{}, maxActive),
		maxBody: maxBody,
	}
}

// Routes returns the HTTP mux with all endpoints registered.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/process", h.handleProcess)
	mux.HandleFunc("/v1/mask", h.handleMask)
	mux.HandleFunc("/v1/restore", h.handleRestore)
	mux.HandleFunc("/livez", h.handleLivez)
	mux.HandleFunc("/readyz", h.handleReadyz)
	return mux
}

// handleProcess implements POST /process. It requires no authentication and
// uses the fixed internal scope.
func (h *Handler) handleProcess(w http.ResponseWriter, r *http.Request) {
	release, ok := h.acquire(w, r)
	if !ok {
		return
	}
	defer release()
	req, ok := h.decodeRequest(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Process(r.Context(), req.PayloadID, req.Payload)
	if err != nil {
		h.writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, processResponse{Result: res.Text})
}

// handleMask implements POST /v1/mask. The consumer is authenticated by its
// Bearer API key and the operation is always a mask.
func (h *Handler) handleMask(w http.ResponseWriter, r *http.Request) {
	release, ok := h.acquire(w, r)
	if !ok {
		return
	}
	defer release()
	consumer, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	req, ok := h.decodeRequest(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Mask(r.Context(), consumer, req.PayloadID, req.Payload)
	if err != nil {
		h.writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, processResponse{Result: res.Text})
}

// handleRestore implements POST /v1/restore. The consumer is authenticated by
// its Bearer API key and must hold the restore right.
func (h *Handler) handleRestore(w http.ResponseWriter, r *http.Request) {
	release, ok := h.acquire(w, r)
	if !ok {
		return
	}
	defer release()
	consumer, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	req, ok := h.decodeRequest(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Restore(r.Context(), consumer, req.PayloadID, req.Payload)
	if err != nil {
		h.writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, processResponse{Result: res.Text})
}

// acquire enforces the active-request limit before the body is read so large
// bodies cannot exhaust memory. It writes a 429 response when the limit is
// reached and returns a release function on success.
func (h *Handler) acquire(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return nil, false
	}
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
// error response and returns ok=false on failure.
func (h *Handler) decodeRequest(w http.ResponseWriter, r *http.Request) (processRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)

	var req processRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return processRequest{}, false
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return processRequest{}, false
	}
	// Reject trailing data so the body is a single JSON object.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must be a single JSON object")
		return processRequest{}, false
	}
	if req.PayloadID == "" {
		writeError(w, http.StatusBadRequest, "payload_id must not be empty")
		return processRequest{}, false
	}
	return req, true
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

// handleLivez reports process liveness.
func (h *Handler) handleLivez(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports readiness: configuration and required dependencies
// are initialized.
func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if h.ready != nil && !h.ready() {
		writeError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
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
