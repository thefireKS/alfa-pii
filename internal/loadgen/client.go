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

// processResponse is the success body for POST /process.
type processResponse struct {
	Result string `json:"result"`
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
	// Latency is the round-trip time of the attempt.
	Latency time.Duration
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
	latency := time.Since(start)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return Response{Outcome: OutcomeTimeout, Latency: latency, Err: err}
		}
		return Response{Outcome: OutcomeError, Latency: latency, Err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Response{Status: resp.StatusCode, Outcome: OutcomeError, Latency: latency, Err: fmt.Errorf("read body: %w", err)}
	}

	r := Response{Status: resp.StatusCode, Latency: latency}
	switch {
	case resp.StatusCode == http.StatusOK:
		var pr processResponse
		if err := json.Unmarshal(raw, &pr); err != nil {
			r.Outcome = OutcomeInvalid
			r.Err = fmt.Errorf("decode response: %w", err)
			return r
		}
		r.Outcome = OutcomeOK
		r.Result = pr.Result
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
// payload_id and text. It returns the last response and the number of attempts
// made. After a final failure of a mask step, no fake restore is created by the
// caller.
func (c *Client) SendWithRetry(ctx context.Context, payloadID, payload string, policy RetryPolicy) (Response, int) {
	var last Response
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		last = c.Send(ctx, payloadID, payload)
		if last.Outcome != OutcomeError && last.Outcome != OutcomeOverload && last.Outcome != OutcomeTimeout {
			return last, attempt
		}
		if attempt == policy.MaxAttempts {
			return last, attempt
		}
		delay := policy.BaseDelay
		if last.Outcome == OutcomeOverload && last.RetryAfter > 0 {
			delay = last.RetryAfter
		}
		select {
		case <-ctx.Done():
			return last, attempt
		case <-time.After(delay):
		}
	}
	return last, policy.MaxAttempts
}