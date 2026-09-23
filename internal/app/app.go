// Package app coordinates the consumer policy, recognition, replacement and
// storage. It owns the direction decision (mask vs restore), the conflict rule
// for a known key, and the per-consumer access policy. The /process operation
// uses a single fixed internal scope; managed consumers are isolated by name.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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

// ErrUnknownConsumer is returned when a consumer name is not configured.
var ErrUnknownConsumer = errors.New("unknown consumer")

// ErrDisabled is returned when a consumer system is disabled.
var ErrDisabled = errors.New("consumer disabled")

// ErrMaskingDisabled is returned when a consumer has masking turned off.
var ErrMaskingDisabled = errors.New("masking disabled for consumer")

// ErrRestoreForbidden is returned when a consumer lacks the restore right.
var ErrRestoreForbidden = errors.New("restore forbidden for consumer")

// ErrNotFound is returned when no correspondence exists for a key.
var ErrNotFound = errors.New("correspondence not found")

// Mask formats. The marker format uses distinguishable markers and supports
// substitution of reordered or repeated own markers. The stars format uses a
// fixed run of asterisks that is ambiguous in modified text, so it is
// restricted to exact restoration of the returned mask.
const (
	FormatMarker = "marker"
	FormatStars  = "stars"
)

// ConsumerScope is the internal key dimension "consumer + payload_id". For
// /process a single fixed scope is used and is never chosen from request
// headers or fields.
const ConsumerScope = "process"

// loggerKey is the context key for a request-scoped logger carrying the
// technical request identifier. It is stored separately from the user-supplied
// payload_id so logs never expose the payload identifier.
type loggerKey struct{}

// WithLogger returns a context carrying the request-scoped logger.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// loggerFrom returns the request-scoped logger from ctx, or the default logger
// when none is set.
func loggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// Consumer is the runtime policy for one consumer system. It is passed
// explicitly between layers so it is never lost.
type Consumer struct {
	// Name identifies the consumer server-side.
	Name string
	// Enabled gates all operations for the consumer.
	Enabled bool
	// Types is the set of data types the consumer protects.
	Types []recognizer.Type
	// MaskingEnabled gates the mask operation.
	MaskingEnabled bool
	// CanRestore gates the restore operation.
	CanRestore bool
	// MaskFormat is the format used to produce masks for this consumer.
	MaskFormat string
}

// Recognizer is the recognition dependency used by the application.
type Recognizer interface {
	Type() recognizer.Type
	Find(text string) ([]recognizer.Fragment, error)
}

// Store is the storage dependency used by the application.
type Store interface {
	Get(key string) (store.Record, bool)
	Create(ctx context.Context, key string, minSize int64, build func(context.Context) (store.Record, error)) (store.Record, bool, error)
	// MarkRestored transitions a record to the replay phase after a successful
	// restore, tied to the record version the caller read. It is a no-op when
	// the record was re-created for the same key or is already in replay.
	MarkRestored(key string, version uint64) bool
}

// Registry builds recognizer sets and priorities for configured types.
type Registry interface {
	Recognizers(types []recognizer.Type) ([]recognizer.Recognizer, func(recognizer.Type) int, error)
}

// Service implements the process, mask and restore operations.
type Service struct {
	recognizers   []Recognizer
	processStore  Store
	consumerStore Store
	masker        *masker.Masker
	consumers     map[string]Consumer
	registry      Registry
}

// New returns a Service wired with the given dependencies for the fixed
// /process scope. No managed consumers are configured. The same store backs
// both the process scope and (unused) consumer operations.
func New(recognizers []Recognizer, st Store, m *masker.Masker) *Service {
	return &Service{
		recognizers:   recognizers,
		processStore:  st,
		consumerStore: st,
		masker:        m,
		consumers:     make(map[string]Consumer),
	}
}

// NewManaged returns a Service for the fixed /process scope plus the given
// managed consumers. The process scope uses the recognizers built from
// processTypes via the registry and its own store, so the unauthenticated
// /process endpoint cannot exhaust the capacity of the managed consumers'
// store. Consumers are keyed by name.
func NewManaged(registry Registry, processTypes []recognizer.Type, consumers []Consumer, processStore, consumerStore Store, m *masker.Masker) (*Service, error) {
	recs, _, err := registry.Recognizers(processTypes)
	if err != nil {
		return nil, fmt.Errorf("build process recognizers: %w", err)
	}
	appRecs := make([]Recognizer, 0, len(recs))
	for _, r := range recs {
		appRecs = append(appRecs, r)
	}
	byName := make(map[string]Consumer, len(consumers))
	for _, c := range consumers {
		// A consumer name must not collide with the reserved /process scope or
		// contain the key separator, otherwise its store key would alias the
		// public scope or another consumer's namespace.
		if c.Name == ConsumerScope {
			return nil, fmt.Errorf("consumer name %q collides with reserved scope %q", c.Name, ConsumerScope)
		}
		if strings.Contains(c.Name, ":") {
			return nil, fmt.Errorf("consumer name %q must not contain %q", c.Name, ":")
		}
		// Validate every configured type before the service is built so an
		// unknown type never activates partially.
		if _, _, err := registry.Recognizers(c.Types); err != nil {
			return nil, fmt.Errorf("consumer %q: %w", c.Name, err)
		}
		byName[c.Name] = c
	}
	return &Service{
		recognizers:   appRecs,
		processStore:  processStore,
		consumerStore: consumerStore,
		masker:        m,
		consumers:     byName,
		registry:      registry,
	}, nil
}

// Result is the outcome of an operation.
type Result struct {
	// Text is the masked text for a mask operation, or the restored text for
	// a restore operation.
	Text string
	// Outcome classifies the result for observability: a new mask, a
	// restoration, or a repeat of a stored result.
	Outcome Outcome
}

// Outcome classifies the result of an operation for observability. The set is
// fixed and bounded.
type Outcome string

const (
	// OutcomeMask means a new mask was created.
	OutcomeMask Outcome = "mask"
	// OutcomeRestore means a restoration was performed.
	OutcomeRestore Outcome = "restore"
	// OutcomeRepeat means a stored result was returned for a repeat of the
	// original or mask.
	OutcomeRepeat Outcome = "repeat"
)

// Process handles one payload for the given payloadID in the fixed /process
// scope. It returns the masked text for a new original, the restored original
// for a repeated mask, or ErrConflict when the text matches neither.
func (s *Service) Process(ctx context.Context, payloadID, payload string) (Result, error) {
	log := loggerFrom(ctx)
	key := ConsumerScope + ":" + payloadID

	rec, ok := s.processStore.Get(key)
	if !ok {
		log.Info("stage", "stage", "operation", "direction", "mask_new")
		var created bool
		var err error
		rec, created, err = s.create(ctx, s.processStore, key, len(payload), func(ctx context.Context) (store.Record, error) {
			if err := ctx.Err(); err != nil {
				return store.Record{}, err
			}
			masked, table, types, err := s.mask(payload)
			if err != nil {
				return store.Record{}, err
			}
			log.Info("stage", "stage", "recognition", "types", types)
			log.Info("stage", "stage", "replacement", "types", types)
			return store.Record{Original: payload, Masked: masked, Table: toStoreTable(table), Format: FormatMarker}, nil
		})
		if err != nil {
			return Result{}, err
		}
		log.Info("stage", "stage", "storage")
		if created {
			return Result{Text: rec.Masked, Outcome: OutcomeMask}, nil
		}
	}

	switch {
	case payload == rec.Original:
		log.Info("stage", "stage", "operation", "direction", "mask_repeat")
		return Result{Text: rec.Masked, Outcome: OutcomeRepeat}, nil
	case payload == rec.Masked:
		log.Info("stage", "stage", "operation", "direction", "restore")
		// A successful restore moves the record to the replay phase so a lost
		// response can be repeated. When Original == Masked the direction is
		// indistinguishable (the no-PII case): the response is stable and the
		// record keeps its normal TTL, so no transition is performed.
		if rec.Original != rec.Masked {
			s.processStore.MarkRestored(key, rec.Version)
		}
		return Result{Text: rec.Original, Outcome: OutcomeRestore}, nil
	default:
		return Result{}, ErrConflict
	}
}

// Mask handles one payload for the given payloadID in the named consumer's
// scope. The operation is defined by the endpoint: it always masks and never
// switches to demasking when presented with a previously issued mask. Current
// access rights (enabled, masking enabled) are checked on every call.
func (s *Service) Mask(ctx context.Context, consumerName, payloadID, payload string) (Result, error) {
	log := loggerFrom(ctx)
	c, err := s.consumer(consumerName)
	if err != nil {
		return Result{}, err
	}
	if !c.MaskingEnabled {
		return Result{}, ErrMaskingDisabled
	}
	key := consumerName + ":" + payloadID

	rec, ok := s.consumerStore.Get(key)
	if !ok {
		log.Info("stage", "stage", "operation", "direction", "mask_new")
		var created bool
		var err error
		rec, created, err = s.create(ctx, s.consumerStore, key, len(payload), func(ctx context.Context) (store.Record, error) {
			if err := ctx.Err(); err != nil {
				return store.Record{}, err
			}
			masked, table, types, err := s.maskFor(c, payload)
			if err != nil {
				return store.Record{}, err
			}
			log.Info("stage", "stage", "recognition", "types", types)
			log.Info("stage", "stage", "replacement", "types", types)
			return store.Record{Original: payload, Masked: masked, Table: toStoreTable(table), Format: c.MaskFormat}, nil
		})
		if err != nil {
			return Result{}, err
		}
		log.Info("stage", "stage", "storage")
		if created {
			return Result{Text: rec.Masked, Outcome: OutcomeMask}, nil
		}
	}

	switch {
	case payload == rec.Original:
		log.Info("stage", "stage", "operation", "direction", "mask_repeat")
		return Result{Text: rec.Masked, Outcome: OutcomeRepeat}, nil
	case payload == rec.Masked:
		// The endpoint is mask: a repeated mask stays a mask and is never
		// switched to demasking.
		log.Info("stage", "stage", "operation", "direction", "mask_repeat")
		return Result{Text: rec.Masked, Outcome: OutcomeRepeat}, nil
	default:
		return Result{}, ErrConflict
	}
}

// Restore substitutes the stored replacement table of the named consumer into
// the given text for the given payloadID. The text may be a new text with
// reordered or repeated own markers. The stored original is never returned
// wholesale instead of substitution. Current access rights (enabled, restore
// right) are checked on every call.
func (s *Service) Restore(ctx context.Context, consumerName, payloadID, masked string) (Result, error) {
	log := loggerFrom(ctx)
	c, err := s.consumer(consumerName)
	if err != nil {
		return Result{}, err
	}
	if !c.CanRestore {
		return Result{}, ErrRestoreForbidden
	}
	key := consumerName + ":" + payloadID

	rec, ok := s.consumerStore.Get(key)
	if !ok {
		return Result{}, ErrNotFound
	}

	switch rec.Format {
	case FormatStars:
		// Star runs are ambiguous in modified text, so restoration is exact
		// only: the input must equal the stored mask.
		if masked != rec.Masked {
			return Result{}, ErrConflict
		}
		log.Info("stage", "stage", "restoration")
		s.consumerStore.MarkRestored(key, rec.Version)
		return Result{Text: rec.Original, Outcome: OutcomeRestore}, nil
	default:
		// Marker format: substitute the consumer's own markers inside the
		// given text. Markers not in the table are left untouched.
		log.Info("stage", "stage", "restoration")
		restored := masker.Restore(masked, fromStoreTable(rec.Original, rec.Table))
		// A restore that substituted no marker (the input had none of the
		// consumer's own markers) is not a genuine restoration, so it does not
		// complete the pair or extend the record's lifetime.
		if restored != masked {
			s.consumerStore.MarkRestored(key, rec.Version)
		}
		return Result{Text: restored, Outcome: OutcomeRestore}, nil
	}
}

// consumer returns the consumer policy for a name, checking that it exists and
// is enabled.
func (s *Service) consumer(name string) (Consumer, error) {
	c, ok := s.consumers[name]
	if !ok {
		return Consumer{}, ErrUnknownConsumer
	}
	if !c.Enabled {
		return Consumer{}, ErrDisabled
	}
	return c, nil
}

// mask runs all recognizers, resolves overlaps and replaces the found
// fragments. A recognition or resolution failure is returned so the operation
// fails closed and no partially masked text is produced. It returns the masked
// text, the replacement table and the distinct entity types found.
func (s *Service) mask(text string) (string, []masker.Replacement, []recognizer.Type, error) {
	var frags []recognizer.Fragment
	for _, r := range s.recognizers {
		fs, err := r.Find(text)
		if err != nil {
			return "", nil, nil, fmt.Errorf("recognize %s: %w", r.Type(), err)
		}
		frags = append(frags, fs...)
	}
	resolved, err := recognizer.Resolve(text, frags)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve fragments: %w", err)
	}
	ranges := make([]masker.Range, 0, len(resolved))
	types := make([]recognizer.Type, 0, len(resolved))
	for _, f := range resolved {
		ranges = append(ranges, masker.Range{Start: f.Start, End: f.End})
		types = append(types, f.Type)
	}
	masked, table := s.masker.Mask(text, ranges)
	return masked, table, types, nil
}

// maskFor masks text for a consumer using its configured types and format.
func (s *Service) maskFor(c Consumer, text string) (string, []masker.Replacement, []recognizer.Type, error) {
	if s.registry == nil {
		return "", nil, nil, errors.New("registry not configured")
	}
	recs, priority, err := s.registry.Recognizers(c.Types)
	if err != nil {
		return "", nil, nil, err
	}
	var frags []recognizer.Fragment
	for _, r := range recs {
		fs, err := r.Find(text)
		if err != nil {
			return "", nil, nil, fmt.Errorf("recognize %s: %w", r.Type(), err)
		}
		frags = append(frags, fs...)
	}
	resolved, err := recognizer.ResolveWithPriority(text, frags, priority)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve fragments: %w", err)
	}
	ranges := make([]masker.Range, 0, len(resolved))
	types := make([]recognizer.Type, 0, len(resolved))
	for _, f := range resolved {
		ranges = append(ranges, masker.Range{Start: f.Start, End: f.End})
		types = append(types, f.Type)
	}
	if c.MaskFormat == FormatStars {
		masked, table := masker.Stars(text, ranges)
		return masked, table, types, nil
	}
	masked, table := s.masker.Mask(text, ranges)
	return masked, table, types, nil
}

// create inserts a new record into the given store, mapping capacity and busy
// errors to the application-level errors. It reports whether this call created
// the record (true) or another goroutine published it first (false). originalLen
// is the length of the payload, used to compute the lower bound of the record's
// memory footprint so the store can reserve capacity before recognition.
func (s *Service) create(ctx context.Context, st Store, key string, originalLen int, build func(context.Context) (store.Record, error)) (store.Record, bool, error) {
	minSize := store.MinRecordSize(key, originalLen)
	createdRec, created, err := st.Create(ctx, key, minSize, build)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrCapacity):
			return store.Record{}, false, ErrCapacity
		case errors.Is(err, store.ErrBusy):
			return store.Record{}, false, ErrBusy
		default:
			return store.Record{}, false, fmt.Errorf("create correspondence: %w", err)
		}
	}
	return createdRec, created, nil
}

func toStoreTable(table []masker.Replacement) []store.Replacement {
	out := make([]store.Replacement, 0, len(table))
	for _, r := range table {
		out = append(out, store.Replacement{Marker: r.Marker, Start: r.Start, End: r.End})
	}
	return out
}

func fromStoreTable(original string, table []store.Replacement) []masker.Replacement {
	out := make([]masker.Replacement, 0, len(table))
	for _, r := range table {
		out = append(out, masker.Replacement{Marker: r.Marker, Original: original[r.Start:r.End]})
	}
	return out
}
