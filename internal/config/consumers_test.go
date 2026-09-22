package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "consumers.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestLoadConsumersValid(t *testing.T) {
	t.Setenv("PII_CONSUMER_ALPHA_SECRET", "alpha-secret")
	t.Setenv("PII_CONSUMER_BETA_SECRET", "beta-secret")
	path := writeTempFile(t, `{
		"consumers": [
			{"name":"alpha","enabled":true,"types":["email","phone"],"masking_enabled":true,"can_restore":true,"mask_format":"marker","secret_env":"PII_CONSUMER_ALPHA_SECRET"},
			{"name":"beta","enabled":true,"types":["card"],"masking_enabled":true,"can_restore":false,"mask_format":"stars","secret_env":"PII_CONSUMER_BETA_SECRET"}
		],
		"regexp_rules": [
			{"type":"contract_number","priority":65,"pattern":"договор №[0-9]{6}","max_matches":100}
		]
	}`)
	consumers, rules, err := LoadConsumers(path)
	if err != nil {
		t.Fatalf("LoadConsumers: %v", err)
	}
	if len(consumers) != 2 {
		t.Fatalf("consumers = %d, want 2", len(consumers))
	}
	if consumers[0].Name != "alpha" || consumers[0].Secret != "alpha-secret" {
		t.Fatalf("alpha = %+v", consumers[0])
	}
	if consumers[1].MaskFormat != "stars" || consumers[1].CanRestore {
		t.Fatalf("beta = %+v", consumers[1])
	}
	if len(rules) != 1 || rules[0].Type != "contract_number" || rules[0].MaxMatches != 100 {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestLoadConsumersSecretFromFile(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretPath, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	path := writeTempFile(t, `{
		"consumers": [
			{"name":"alpha","enabled":true,"types":["email"],"masking_enabled":true,"can_restore":true,"mask_format":"marker","secret_file":"`+secretPath+`"}
		]
	}`)
	consumers, _, err := LoadConsumers(path)
	if err != nil {
		t.Fatalf("LoadConsumers: %v", err)
	}
	if consumers[0].Secret != "file-secret" {
		t.Fatalf("secret = %q, want file-secret", consumers[0].Secret)
	}
}

func TestLoadConsumersRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		content string
		env     map[string]string
	}{
		{name: "missing secret", content: `{"consumers":[{"name":"a","enabled":true,"types":["email"],"masking_enabled":true,"can_restore":true,"mask_format":"marker"}]}`},
		{name: "empty secret env", content: `{"consumers":[{"name":"a","enabled":true,"types":["email"],"masking_enabled":true,"can_restore":true,"mask_format":"marker","secret_env":"PII_MISSING"}]}`},
		{name: "both secret sources", content: `{"consumers":[{"name":"a","enabled":true,"types":["email"],"masking_enabled":true,"can_restore":true,"mask_format":"marker","secret_env":"PII_A","secret_file":"/x"}]}`, env: map[string]string{"PII_A": "v"}},
		{name: "no types", content: `{"consumers":[{"name":"a","enabled":true,"types":[],"masking_enabled":true,"can_restore":true,"mask_format":"marker","secret_env":"PII_A"}]}`, env: map[string]string{"PII_A": "v"}},
		{name: "bad format", content: `{"consumers":[{"name":"a","enabled":true,"types":["email"],"masking_enabled":true,"can_restore":true,"mask_format":"bogus","secret_env":"PII_A"}]}`, env: map[string]string{"PII_A": "v"}},
		{name: "duplicate name", content: `{"consumers":[{"name":"a","enabled":true,"types":["email"],"masking_enabled":true,"can_restore":true,"mask_format":"marker","secret_env":"PII_A"},{"name":"a","enabled":true,"types":["phone"],"masking_enabled":true,"can_restore":true,"mask_format":"marker","secret_env":"PII_A"}]}`, env: map[string]string{"PII_A": "v"}},
		{name: "empty rule type", content: `{"regexp_rules":[{"type":"","priority":1,"pattern":"x","max_matches":1}]}`},
		{name: "empty rule pattern", content: `{"regexp_rules":[{"type":"t","priority":1,"pattern":"","max_matches":1}]}`},
		{name: "zero rule max matches", content: `{"regexp_rules":[{"type":"t","priority":1,"pattern":"x","max_matches":0}]}`},
		{name: "malformed json", content: `{not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			path := writeTempFile(t, tt.content)
			if _, _, err := LoadConsumers(path); err == nil {
				t.Fatalf("expected error for %q", tt.name)
			}
		})
	}
}

func TestLoadConsumersMissingFile(t *testing.T) {
	if _, _, err := LoadConsumers(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
