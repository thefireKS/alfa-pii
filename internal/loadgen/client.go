package loadgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// processRequest is the request body for POST /process.
type processRequest struct {
	Payload   string `json:"payload"`
	PayloadID string `json:"payload_id"`
}

// Outcome classifies the result of one request attempt.
type Outcome string

// Request outcomes. The set is fixed and bounded.
const (
	OutcomeOK       Outcome = "ok"       // HTTP 200 with a valid string result
	OutcomeMask     Outcome = "mask"     // a new mask was created (classified)
	OutcomeRestore  Outcome = "restore"  // a restoration was performed (classified)
	OutcomeRepeat   Outcome = "repeat"   // a stored result was returned (classified)
	OutcomeConflict Outcome = "conflict" // 409
	OutcomeOverload Outcome = "overload" // 429
	OutcomeError    Outcome = "error"    // 5xx or transport error
	OutcomeTimeout  Outcome = "timeout" // client timeout
	OutcomeInvalid  Outcome = "invalid" // malformed response
)

// Response is the classified result of one request attempt.
type Response struct {
	// Status is the HTTP status code, or 0 for a transport error.
	Status int
	// Outcome classifies the response.
	Outcome Outcome
	// Result is the string result on success.
	Result string
	// RetryAfter is the parsed Retry-After header in seconds, when present.
	RetryAfter time.Duration
	// Err is the transport or decode error, when any.
	Err error
	// Latency is the round-trip time of the attempt, measured until the full
	// response body has been read. It includes the time spent waiting for and
	// reading the body, not just the headers.
	Latency time.Duration
	// TTFB is the time to first byte: the interval until the response headers
	// arrive. It is a separate measurement from Latency, which also covers the
	// body read.
	TTFB time.Duration
}

// IsValidSuccess reports whether the response is a well-formed success: HTTP
// 200, valid JSON with a string result. It is used by the compatibility check.
func (r Response) IsValidSuccess() bool {
	return r.Status == http.StatusOK && r.Outcome != OutcomeInvalid && r.Outcome != OutcomeError && r.Outcome != OutcomeTimeout
}

// Client sends /process requests with retry logic. It reuses a single HTTP
// transport so connections are pooled. The client timeout is fixed at 10s per
// the service contract.
type Client struct {
	http    *http.Client
	baseURL string
	timeout time.Duration
}

// NewClient builds a Client for the given base URL. maxConns bounds the number
// of pooled connections per host. timeout bounds each request; the default is
// 10s.
func NewClient(baseURL string, maxConns int, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		MaxIdleConns:        maxConns,
		MaxIdleConnsPerHost: maxConns,
		MaxConnsPerHost:     maxConns,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		http:    &http.Client{Transport: transport, Timeout: timeout},
		baseURL: baseURL,
		timeout: timeout,
	}
}

// Send performs one /process request with the given payload and payload_id. It
// returns the classified response. No retry is applied here; retries are
// orchestrated by the caller so the same ID and text are reused.
func (c *Client) Send(ctx context.Context, payloadID, payload string) Response {
	body, err := json.Marshal(processRequest{Payload: payload, PayloadID: payloadID})
	if err != nil {
		return Response{Outcome: OutcomeError, Err: fmt.Errorf("marshal request: %w", err)}
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/process", bytes.NewReader(body))
	if err != nil {
		return Response{Outcome: OutcomeError, Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	ttfb := time.Since(start)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return Response{Outcome: OutcomeTimeout, Latency: ttfb, TTFB: ttfb, Err: err}
		}
		return Response{Outcome: OutcomeError, Latency: ttfb, TTFB: ttfb, Err: err}
	}
	defer resp.Body.Close()

	raw, err := readBodyLimited(resp.Body, maxResponseBytes)
	if err != nil {
		return Response{Status: resp.StatusCode, Outcome: OutcomeError, Latency: time.Since(start), TTFB: ttfb, Err: fmt.Errorf("read body: %w", err)}
	}
	// Latency is measured after the full body has been read so the time spent
	// waiting for and reading the body is included in the round-trip time.
	latency := time.Since(start)

	r := Response{Status: resp.StatusCode, Latency: latency, TTFB: ttfb}
	switch {
	case resp.StatusCode == http.StatusOK:
		result, ok := decodeResult(raw)
		if !ok {
			r.Outcome = OutcomeInvalid
			r.Err = fmt.Errorf("invalid result in response")
			return r
		}
		r.Outcome = OutcomeOK
		r.Result = result
	case resp.StatusCode == http.StatusTooManyRequests:
		r.Outcome = OutcomeOverload
		r.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	case resp.StatusCode == http.StatusConflict:
		r.Outcome = OutcomeConflict
	case resp.StatusCode >= 500:
		r.Outcome = OutcomeError
	default:
		r.Outcome = OutcomeInvalid
		r.Err = fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return r
}

// isTimeout reports whether err is a net timeout.
func isTimeout(err error) bool {
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}

// parseRetryAfter parses the Retry-After header as a number of seconds. A
// missing or malformed value yields 0.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// maxResponseBytes bounds the size of a response body the client will read.
const maxResponseBytes = 1 << 20

// readBodyLimited reads the body up to maxResponseBytes and reports an error
// when the body exceeds the limit, so a truncated response is never silently
// accepted as a complete one.
func readBodyLimited(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return raw, nil
}

// decodeResult extracts the string result from a valid JSON response. It
// reports false when the result field is missing, is the JSON null literal, or
// is not a JSON string. An empty string is a valid result, so {"result":""} is
// accepted while {} and {"result":null} are rejected.
func decodeResult(raw []byte) (string, bool) {
	var pr struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return "", false
	}
	if len(pr.Result) == 0 || bytes.Equal(bytes.TrimSpace(pr.Result), []byte("null")) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(pr.Result, &s); err != nil {
		return "", false
	}
	return s, true
}

// RetryPolicy bounds retries for a single logical operation. A retry reuses the
// same payload_id and text. At most MaxAttempts attempts are made; after a 429
// the Retry-After delay is respected.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	MaxAttempts int
	// BaseDelay is the base backoff between non-429 retries.
	BaseDelay time.Duration
}

// DefaultRetryPolicy allows up to three attempts with a short base backoff.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: 200 * time.Millisecond}
}

// SendWithRetry sends a request and retries on error or 429, reusing the same
// payload_id and text. It returns the final response and every attempt made, so
// the caller can account for each actual HTTP send and its outcome. After a
// final failure of a mask step, no fake restore is created by the caller.
func (c *Client) SendWithRetry(ctx context.Context, payloadID, payload string, policy RetryPolicy) (Response, []Response) {
	var attempts []Response
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		resp := c.Send(ctx, payloadID, payload)
		attempts = append(attempts, resp)
		if resp.Outcome != OutcomeError && resp.Outcome != OutcomeOverload && resp.Outcome != OutcomeTimeout {
			return resp, attempts
		}
		if attempt == policy.MaxAttempts {
			return resp, attempts
		}
		delay := policy.BaseDelay
		if resp.Outcome == OutcomeOverload && resp.RetryAfter > 0 {
			delay = resp.RetryAfter
		}
		select {
		case <-ctx.Done():
			return resp, attempts
		case <-time.After(delay):
		}
	}
	return attempts[len(attempts)-1], attempts
}