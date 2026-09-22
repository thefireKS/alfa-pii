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

	"alfa-hackathon.local/pii/internal/app"
)

// processRequest is the accepted request body for POST /process.
type processRequest struct {
	Payload   string `json:"payload"`
	PayloadID string `json:"payload_id"`
}

// processResponse is the success body for POST /process.
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
}

// Handler holds the HTTP dependencies and the readiness state.
type Handler struct {
	svc     Service
	ready   func() bool
	active  chan struct{}
	maxBody int64
}

// NewHandler builds the HTTP handler with the given dependencies.
func NewHandler(svc Service, ready func() bool, maxActive int, maxBody int64) *Handler {
	return &Handler{
		svc:     svc,
		ready:   ready,
		active:  make(chan struct{}, maxActive),
		maxBody: maxBody,
	}
}

// Routes returns the HTTP mux with all endpoints registered.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/process", h.handleProcess)
	mux.HandleFunc("/livez", h.handleLivez)
	mux.HandleFunc("/readyz", h.handleReadyz)
	return mux
}

// handleProcess implements POST /process.
func (h *Handler) handleProcess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Enforce the active-request limit before reading the body so large
	// bodies cannot exhaust memory.
	select {
	case h.active <- struct{}{}:
		defer func() { <-h.active }()
	default:
		writeRetryAfter(w, "too many requests")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)

	var req processRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Reject trailing data so the body is a single JSON object.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request body must be a single JSON object")
		return
	}
	if req.PayloadID == "" {
		writeError(w, http.StatusBadRequest, "payload_id must not be empty")
		return
	}

	res, err := h.svc.Process(r.Context(), req.PayloadID, req.Payload)
	if err != nil {
		switch {
		case errors.Is(err, app.ErrConflict):
			writeError(w, http.StatusConflict, "text conflicts with existing correspondence")
		case errors.Is(err, app.ErrCapacity):
			writeRetryAfter(w, "storage capacity exceeded")
		case errors.Is(err, app.ErrBusy):
			writeRetryAfter(w, "storage busy creating key")
		default:
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writeJSON(w, http.StatusOK, processResponse{Result: res.Text})
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
