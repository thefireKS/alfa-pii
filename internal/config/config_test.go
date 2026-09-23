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
	cfg.StoreMaxRecordBytes = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero max record bytes")
	}
	cfg = Default()
	cfg.StoreCreateWait = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero create wait")
	}
	cfg = Default()
	cfg.StoreCleanupInterval = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero cleanup interval")
	}
	cfg = Default()
	cfg.MaxWorkingBytes = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero max working bytes")
	}
	cfg = Default()
	cfg.MarkerPrefix = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty marker prefix")
	}
}
