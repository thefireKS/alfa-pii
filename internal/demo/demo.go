// Package demo provides a reproducible end-to-end demonstration of the
// masking -> LLM -> restore cycle over the existing /v1/mask and /v1/restore
// endpoints. A real network and provider key are not required: the model is a
// deterministic in-process implementation behind a small interface. The demo
// never prints the authentication secret and never sends the original text to
// the model.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Model is the small interface for the demo LLM. A real provider is not
// required; the demo uses a deterministic in-process implementation.
type Model interface {
	// Complete returns the model's answer for the given prompt. The prompt is
	// the masked text, never the original sensitive values.
	Complete(ctx context.Context, prompt string) (string, error)
}

// Masker masks a payload for a payload ID via /v1/mask.
type Masker interface {
	Mask(ctx context.Context, payloadID, payload string) (string, error)
}

// Restorer restores markers in a payload for a payload ID via /v1/restore.
type Restorer interface {
	Restore(ctx context.Context, payloadID, payload string) (string, error)
}

// Client calls the /v1/mask and /v1/restore endpoints of a running service.
// The consumer identity is carried by the Bearer token; the secret is never
// logged or printed.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient returns a Client for the given base URL and consumer token.
func NewClient(baseURL, token string) *Client {
	return &Client{baseURL: baseURL, token: token, http: &http.Client{}}
}

// Mask implements Masker.
func (c *Client) Mask(ctx context.Context, payloadID, payload string) (string, error) {
	return c.call(ctx, "/v1/mask", payloadID, payload)
}

// Restore implements Restorer.
func (c *Client) Restore(ctx context.Context, payloadID, payload string) (string, error) {
	return c.call(ctx, "/v1/restore", payloadID, payload)
}

// call performs one authenticated POST to the given endpoint.
func (c *Client) call(ctx context.Context, path, payloadID, payload string) (string, error) {
	body, err := json.Marshal(map[string]string{"payload": payload, "payload_id": payloadID})
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call %s: %w", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read %s response: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		// The error body never contains the original text or found values.
		return "", fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("decode %s response: %w", path, err)
	}
	return out.Result, nil
}

// Runner orchestrates the demo scenario: mask the original, ask the model for
// an answer to the mask, then restore the answer for the consumer.
type Runner struct {
	masker   Masker
	restorer Restorer
	model    Model
}

// NewRunner returns a Runner wired with the given dependencies.
func NewRunner(masker Masker, restorer Restorer, model Model) *Runner {
	return &Runner{masker: masker, restorer: restorer, model: model}
}

// Result holds the outputs of one demo scenario.
type Result struct {
	// Original is the synthetic input text.
	Original string
	// Masked is the protected text sent to the model.
	Masked string
	// ModelResponse is the model's answer, signed as a demonstration.
	ModelResponse string
	// Restored is the answer with the consumer's own markers substituted.
	Restored string
}

// Run performs the full cycle. If masking fails, the operation fails closed and
// the model is never called. If the model fails, the error is returned and no
// restore is attempted.
func (r *Runner) Run(ctx context.Context, payloadID, original string) (Result, error) {
	masked, err := r.masker.Mask(ctx, payloadID, original)
	if err != nil {
		return Result{}, fmt.Errorf("mask: %w", err)
	}
	modelResp, err := r.model.Complete(ctx, masked)
	if err != nil {
		return Result{}, fmt.Errorf("model: %w", err)
	}
	restored, err := r.restorer.Restore(ctx, payloadID, modelResp)
	if err != nil {
		return Result{}, fmt.Errorf("restore: %w", err)
	}
	return Result{
		Original:      original,
		Masked:        masked,
		ModelResponse: modelResp,
		Restored:      restored,
	}, nil
}

// DemoModel is a deterministic in-process LLM for the demonstration. It parses
// the markers in the prompt and returns an answer that reorders the markers,
// repeats one of them and drops another, so restoration is exercised inside a
// modified answer. The answer is signed as a demonstration.
type DemoModel struct{}

// Complete implements Model. It never echoes the prompt verbatim; it only
// rearranges the markers it found.
func (DemoModel) Complete(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	markers := extractMarkers(prompt)
	if len(markers) == 0 {
		return "[demo] В запросе нет персональных данных.", nil
	}
	first := markers[0]
	last := markers[len(markers)-1]
	if len(markers) == 1 {
		return "[demo] Проверено: " + first + ".", nil
	}
	// Reorder (last before first), repeat the first marker, and drop the
	// middle markers. Restoration must substitute only the markers present in
	// this answer.
	return "[demo] Проверено: " + last + " и " + first + ", повтор " + first + ".", nil
}

// markerRe matches the distinguishable marker format [PREFIX_N].
var markerRe = regexp.MustCompile(`\[[A-Za-z]+_[0-9]+\]`)

// extractMarkers returns the distinct markers in text ordered by their numeric
// index, so the demo answer is deterministic regardless of prompt order.
func extractMarkers(text string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range markerRe.FindAllString(text, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return markerIndex(out[i]) < markerIndex(out[j])
	})
	return out
}

// markerIndex returns the numeric suffix of a marker, or 0 if it cannot be
// parsed. It is only used to order markers deterministically.
func markerIndex(marker string) int {
	idx := strings.LastIndexByte(marker, '_')
	if idx < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSuffix(marker[idx+1:], "]"))
	if err != nil {
		return 0
	}
	return n
}
