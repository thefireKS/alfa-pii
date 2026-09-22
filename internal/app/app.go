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

// ConsumerScope is the internal key dimension "consumer + payload_id". For
// /process a single fixed scope is used and is never chosen from request
// headers or fields.
const ConsumerScope = "process"

// Recognizer is the recognition dependency used by the application.
type Recognizer interface {
	Type() recognizer.Type
	Find(text string) []recognizer.Fragment
}

// Store is the storage dependency used by the application.
type Store interface {
	Get(key string) (store.Record, bool)
	Create(key string, rec store.Record) (store.Record, bool, error)
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
// ErrConflict when the text matches neither.
func (s *Service) Process(ctx context.Context, payloadID, payload string) (Result, error) {
	key := ConsumerScope + ":" + payloadID

	rec, ok := s.store.Get(key)
	if !ok {
		masked, table := s.mask(payload)
		newRec := store.Record{Original: payload, Masked: masked, Table: toStoreTable(table)}
		createdRec, created, err := s.create(ctx, key, newRec)
		if err != nil {
			return Result{}, err
		}
		if created {
			return Result{Text: masked}, nil
		}
		rec = createdRec
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

// mask runs all recognizers and replaces the found fragments.
func (s *Service) mask(text string) (string, []masker.Replacement) {
	var ranges []masker.Range
	for _, r := range s.recognizers {
		for _, f := range r.Find(text) {
			ranges = append(ranges, masker.Range{Start: f.Start, End: f.End})
		}
	}
	return s.masker.Mask(text, ranges)
}

// create inserts a new record, mapping capacity errors to ErrCapacity.
func (s *Service) create(ctx context.Context, key string, rec store.Record) (store.Record, bool, error) {
	createdRec, created, err := s.store.Create(key, rec)
	if err != nil {
		if errors.Is(err, store.ErrCapacity) {
			return store.Record{}, false, ErrCapacity
		}
		return store.Record{}, false, fmt.Errorf("create correspondence: %w", err)
	}
	return createdRec, created, nil
}

func toStoreTable(table []masker.Replacement) []store.Replacement {
	out := make([]store.Replacement, 0, len(table))
	for _, r := range table {
		out = append(out, store.Replacement{Marker: r.Marker, Original: r.Original})
	}
	return out
}
