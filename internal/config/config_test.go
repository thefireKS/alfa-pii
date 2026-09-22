package config

import (
	"testing"
	"time"
)

func TestDefaultValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
}

func TestValidateRejectsInvalid(t *testing.T) {
	cfg := Default()
	cfg.MaxBodyBytes = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero max body bytes")
	}
	cfg = Default()
	cfg.StoreTTL = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative TTL")
	}
	cfg = Default()
	cfg.MarkerPrefix = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty marker prefix")
	}
}
