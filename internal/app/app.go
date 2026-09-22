// Package app coordinates the consumer policy, recognition, replacement and
// storage for a single process operation. It owns the direction decision
// (mask vs restore) and the conflict rule for a known key.
package app

import (
	"context"
	"errors"
	"fmt"

	"alfa-hackathon.local/pii/internal/masker"
	"alfa-hackathon.local/pii/internal/recognizer"
	"alfa-hackathon.local/pii/internal/store"
)

// ErrConflict is returned when a known key is presented with text that is
// neither the stored original nor the stored mask.
var ErrConflict = errors.New("text conflicts with existing correspondence")

// ErrCapacity is returned when the store cannot hold a new correspondence.
var ErrCapacity = errors.New("store capacity exceeded")

// ErrBusy is returned when creating a correspondence for a key that another
// request is already creating takes longer than the configured wait limit.
var ErrBusy = errors.New("store busy creating key")

// ConsumerScope is the internal key dimension "consumer + payload_id". For
// /process a single fixed scope is used and is never chosen from request
// headers or fields.
const ConsumerScope = "process"

// Recognizer is the recognition dependency used by the application.
type Recognizer interface {
	Type() recognizer.Type
	Find(text string) ([]recognizer.Fragment, error)
}

// Store is the storage dependency used by the application.
type Store interface {
	Get(key string) (store.Record, bool)
	Create(ctx context.Context, key string, build func(context.Context) (store.Record, error)) (store.Record, bool, error)
}

// Service implements the process operation.
type Service struct {
	recognizers []Recognizer
	store       Store
	masker      *masker.Masker
}

// New returns a Service wired with the given dependencies.
func New(recognizers []Recognizer, st Store, m *masker.Masker) *Service {
	return &Service{recognizers: recognizers, store: st, masker: m}
}

// Result is the outcome of a process operation.
type Result struct {
	// Text is the masked text for a new original, or the restored original
	// for a repeated mask.
	Text string
}

// Process handles one payload for the given payloadID. It returns the masked
// text for a new original, the restored original for a repeated mask, or
// ErrConflict when the text matches neither. Recognition runs only for the
// request that wins the per-key reservation; concurrent requests for the same
// key wait for the winner and observe the same published record.
func (s *Service) Process(ctx context.Context, payloadID, payload string) (Result, error) {
	key := ConsumerScope + ":" + payloadID

	rec, ok := s.store.Get(key)
	if !ok {
		var err error
		rec, err = s.create(ctx, key, func(ctx context.Context) (store.Record, error) {
			if err := ctx.Err(); err != nil {
				return store.Record{}, err
			}
			masked, table, err := s.mask(payload)
			if err != nil {
				return store.Record{}, err
			}
			return store.Record{Original: payload, Masked: masked, Table: toStoreTable(table)}, nil
		})
		if err != nil {
			return Result{}, err
		}
	}

	switch {
	case payload == rec.Original:
		return Result{Text: rec.Masked}, nil
	case payload == rec.Masked:
		return Result{Text: rec.Original}, nil
	default:
		return Result{}, ErrConflict
	}
}

// mask runs all recognizers, resolves overlaps and replaces the found
// fragments. A recognition or resolution failure is returned so the operation
// fails closed and no partially masked text is produced.
func (s *Service) mask(text string) (string, []masker.Replacement, error) {
	var frags []recognizer.Fragment
	for _, r := range s.recognizers {
		fs, err := r.Find(text)
		if err != nil {
			return "", nil, fmt.Errorf("recognize %s: %w", r.Type(), err)
		}
		frags = append(frags, fs...)
	}
	resolved, err := recognizer.Resolve(text, frags)
	if err != nil {
		return "", nil, fmt.Errorf("resolve fragments: %w", err)
	}
	ranges := make([]masker.Range, 0, len(resolved))
	for _, f := range resolved {
		ranges = append(ranges, masker.Range{Start: f.Start, End: f.End})
	}
	masked, table := s.masker.Mask(text, ranges)
	return masked, table, nil
}

// create inserts a new record, mapping capacity and busy errors to the
// application-level errors.
func (s *Service) create(ctx context.Context, key string, build func(context.Context) (store.Record, error)) (store.Record, error) {
	createdRec, _, err := s.store.Create(ctx, key, build)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrCapacity):
			return store.Record{}, ErrCapacity
		case errors.Is(err, store.ErrBusy):
			return store.Record{}, ErrBusy
		default:
			return store.Record{}, fmt.Errorf("create correspondence: %w", err)
		}
	}
	return createdRec, nil
}

func toStoreTable(table []masker.Replacement) []store.Replacement {
	out := make([]store.Replacement, 0, len(table))
	for _, r := range table {
		out = append(out, store.Replacement{Marker: r.Marker, Original: r.Original})
	}
	return out
}
