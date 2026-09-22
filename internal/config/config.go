// Package config holds validated service settings. Configuration is checked
// as a whole before use so that an invalid value never activates partially.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the validated set of runtime settings for the service.
type Config struct {
	// ListenAddr is the TCP address the HTTP server binds to.
	ListenAddr string
	// ReadTimeout bounds reading the whole request, including the body.
	ReadTimeout time.Duration
	// WriteTimeout bounds writing the response.
	WriteTimeout time.Duration
	// IdleTimeout bounds keep-alive connections between requests.
	IdleTimeout time.Duration
	// ShutdownTimeout bounds graceful shutdown of active operations.
	ShutdownTimeout time.Duration

	// MaxBodyBytes caps the request body size. Larger bodies are rejected
	// before they are fully read.
	MaxBodyBytes int64
	// MaxActiveRequests caps concurrent in-flight requests. The limit is
	// enforced before the body is read so that large bodies cannot exhaust
	// memory.
	MaxActiveRequests int

	// StoreMaxEntries caps the number of stored correspondences.
	StoreMaxEntries int
	// StoreMaxBytes caps the total bytes held by stored correspondences.
	StoreMaxBytes int64
	// StoreMaxRecordBytes caps the estimated bytes of a single correspondence.
	StoreMaxRecordBytes int64
	// StoreTTL is how long a correspondence is kept after creation.
	StoreTTL time.Duration
	// StoreCreateWait is the maximum time a request waits for another request
	// creating the same key before the store reports it busy.
	StoreCreateWait time.Duration
	// StoreCleanupInterval is how often the background cleanup evicts expired
	// correspondences.
	StoreCleanupInterval time.Duration

	// MarkerPrefix is the prefix used for generated replacement markers.
	MarkerPrefix string

	// ConsumersFile is the path to the JSON file defining consumer systems and
	// regexp rules. Empty means no managed consumers are configured.
	ConsumersFile string
	// Consumers are the validated managed consumer policies.
	Consumers []Consumer
	// RegexpRules are the validated config-driven recognition rules.
	RegexpRules []RegexpRule
}

// Default returns a Config populated with documented default values.
func Default() Config {
	return Config{
		ListenAddr:           ":8080",
		ReadTimeout:          10 * time.Second,
		WriteTimeout:         10 * time.Second,
		IdleTimeout:          60 * time.Second,
		ShutdownTimeout:      10 * time.Second,
		MaxBodyBytes:         1 << 20, // 1 MiB
		MaxActiveRequests:    200,
		StoreMaxEntries:      100_000,
		StoreMaxBytes:        64 << 20, // 64 MiB
		StoreMaxRecordBytes:  1 << 20,  // 1 MiB per record
		StoreTTL:             24 * time.Hour,
		StoreCreateWait:      5 * time.Second,
		StoreCleanupInterval: time.Minute,
		MarkerPrefix:         "PII",
	}
}

// Load reads settings from the environment and validates them as a whole.
// Unknown or malformed values produce an error and no partial configuration
// is returned.
func Load() (Config, error) {
	cfg := Default()

	if v := os.Getenv("PII_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("PII_READ_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_READ_TIMEOUT: %w", err)
		}
		cfg.ReadTimeout = d
	}
	if v := os.Getenv("PII_WRITE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_WRITE_TIMEOUT: %w", err)
		}
		cfg.WriteTimeout = d
	}
	if v := os.Getenv("PII_IDLE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_IDLE_TIMEOUT: %w", err)
		}
		cfg.IdleTimeout = d
	}
	if v := os.Getenv("PII_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	if v := os.Getenv("PII_MAX_BODY_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("PII_MAX_BODY_BYTES: %w", err)
		}
		cfg.MaxBodyBytes = n
	}
	if v := os.Getenv("PII_MAX_ACTIVE_REQUESTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_MAX_ACTIVE_REQUESTS: %w", err)
		}
		cfg.MaxActiveRequests = n
	}
	if v := os.Getenv("PII_STORE_MAX_ENTRIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_STORE_MAX_ENTRIES: %w", err)
		}
		cfg.StoreMaxEntries = n
	}
	if v := os.Getenv("PII_STORE_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("PII_STORE_MAX_BYTES: %w", err)
		}
		cfg.StoreMaxBytes = n
	}
	if v := os.Getenv("PII_STORE_MAX_RECORD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("PII_STORE_MAX_RECORD_BYTES: %w", err)
		}
		cfg.StoreMaxRecordBytes = n
	}
	if v := os.Getenv("PII_STORE_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_STORE_TTL: %w", err)
		}
		cfg.StoreTTL = d
	}
	if v := os.Getenv("PII_STORE_CREATE_WAIT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_STORE_CREATE_WAIT: %w", err)
		}
		cfg.StoreCreateWait = d
	}
	if v := os.Getenv("PII_STORE_CLEANUP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("PII_STORE_CLEANUP_INTERVAL: %w", err)
		}
		cfg.StoreCleanupInterval = d
	}
	if v := os.Getenv("PII_MARKER_PREFIX"); v != "" {
		cfg.MarkerPrefix = v
	}
	if v := os.Getenv("PII_CONSUMERS_FILE"); v != "" {
		cfg.ConsumersFile = v
	}

	if cfg.ConsumersFile != "" {
		consumers, rules, err := LoadConsumers(cfg.ConsumersFile)
		if err != nil {
			return Config{}, err
		}
		cfg.Consumers = consumers
		cfg.RegexpRules = rules
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the whole configuration for consistency. It returns an
// error describing the first invalid setting found.
func (c Config) Validate() error {
	var errs []error
	if c.ListenAddr == "" {
		errs = append(errs, errors.New("listen address must not be empty"))
	}
	if c.ReadTimeout <= 0 {
		errs = append(errs, errors.New("read timeout must be positive"))
	}
	if c.WriteTimeout <= 0 {
		errs = append(errs, errors.New("write timeout must be positive"))
	}
	if c.IdleTimeout <= 0 {
		errs = append(errs, errors.New("idle timeout must be positive"))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("shutdown timeout must be positive"))
	}
	if c.MaxBodyBytes <= 0 {
		errs = append(errs, errors.New("max body bytes must be positive"))
	}
	if c.MaxActiveRequests <= 0 {
		errs = append(errs, errors.New("max active requests must be positive"))
	}
	if c.StoreMaxEntries <= 0 {
		errs = append(errs, errors.New("store max entries must be positive"))
	}
	if c.StoreMaxBytes <= 0 {
		errs = append(errs, errors.New("store max bytes must be positive"))
	}
	if c.StoreMaxRecordBytes <= 0 {
		errs = append(errs, errors.New("store max record bytes must be positive"))
	}
	if c.StoreTTL <= 0 {
		errs = append(errs, errors.New("store TTL must be positive"))
	}
	if c.StoreCreateWait <= 0 {
		errs = append(errs, errors.New("store create wait must be positive"))
	}
	if c.StoreCleanupInterval <= 0 {
		errs = append(errs, errors.New("store cleanup interval must be positive"))
	}
	if c.MarkerPrefix == "" {
		errs = append(errs, errors.New("marker prefix must not be empty"))
	}
	return errors.Join(errs...)
}
