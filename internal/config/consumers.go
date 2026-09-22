package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"alfa-hackathon.local/pii/internal/recognizer"
)

// Consumer is the validated policy for one consumer system. Secrets are
// resolved from the environment or a file at load time; the repository stores
// only the names of the variables, never the values.
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
	// Secret is the resolved API key used to authenticate the consumer.
	Secret string
}

// RegexpRule is a config-driven recognition rule for a new data type.
type RegexpRule struct {
	// Type is the data type this rule detects.
	Type recognizer.Type
	// Priority orders this type in conflict resolution.
	Priority int
	// Pattern is a standard Go regular expression.
	Pattern string
	// MaxMatches bounds the number of fragments produced per text.
	MaxMatches int
}

// consumersFile is the on-disk JSON schema for consumer configuration.
type consumersFile struct {
	Consumers   []consumerFile   `json:"consumers"`
	RegexpRules []regexpRuleFile `json:"regexp_rules"`
}

type consumerFile struct {
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	Types          []string `json:"types"`
	MaskingEnabled bool     `json:"masking_enabled"`
	CanRestore     bool     `json:"can_restore"`
	MaskFormat     string   `json:"mask_format"`
	SecretEnv      string   `json:"secret_env"`
	SecretFile     string   `json:"secret_file"`
}

type regexpRuleFile struct {
	Type       string `json:"type"`
	Priority   int    `json:"priority"`
	Pattern    string `json:"pattern"`
	MaxMatches int    `json:"max_matches"`
}

// LoadConsumers reads consumer and regexp-rule configuration from a JSON file
// and resolves secrets. The whole file is validated before any consumer is
// returned so an invalid configuration never activates partially.
func LoadConsumers(path string) ([]Consumer, []RegexpRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read consumers file: %w", err)
	}
	var raw consumersFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse consumers file: %w", err)
	}

	consumers := make([]Consumer, 0, len(raw.Consumers))
	seen := make(map[string]bool)
	for _, cf := range raw.Consumers {
		secret, err := resolveSecret(cf.SecretEnv, cf.SecretFile)
		if err != nil {
			return nil, nil, fmt.Errorf("consumer %q: %w", cf.Name, err)
		}
		types := make([]recognizer.Type, 0, len(cf.Types))
		for _, t := range cf.Types {
			if strings.TrimSpace(t) == "" {
				return nil, nil, fmt.Errorf("consumer %q: empty type name", cf.Name)
			}
			types = append(types, recognizer.Type(t))
		}
		c := Consumer{
			Name:           cf.Name,
			Enabled:        cf.Enabled,
			Types:          types,
			MaskingEnabled: cf.MaskingEnabled,
			CanRestore:     cf.CanRestore,
			MaskFormat:     cf.MaskFormat,
			Secret:         secret,
		}
		if err := validateConsumer(c); err != nil {
			return nil, nil, err
		}
		if seen[c.Name] {
			return nil, nil, fmt.Errorf("duplicate consumer name %q", c.Name)
		}
		seen[c.Name] = true
		consumers = append(consumers, c)
	}

	rules := make([]RegexpRule, 0, len(raw.RegexpRules))
	for _, rf := range raw.RegexpRules {
		r := RegexpRule{
			Type:       recognizer.Type(rf.Type),
			Priority:   rf.Priority,
			Pattern:    rf.Pattern,
			MaxMatches: rf.MaxMatches,
		}
		if err := validateRegexpRule(r); err != nil {
			return nil, nil, err
		}
		rules = append(rules, r)
	}

	return consumers, rules, nil
}

// resolveSecret reads a secret from the environment variable named by envName
// or from the file at filePath. Exactly one source must be configured.
func resolveSecret(envName, filePath string) (string, error) {
	if envName != "" && filePath != "" {
		return "", errors.New("both secret_env and secret_file are set; configure exactly one")
	}
	if envName != "" {
		v := os.Getenv(envName)
		if v == "" {
			return "", fmt.Errorf("secret environment variable %q is empty", envName)
		}
		return v, nil
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read secret file: %w", err)
		}
		v := strings.TrimSpace(string(data))
		if v == "" {
			return "", fmt.Errorf("secret file %q is empty", filePath)
		}
		return v, nil
	}
	return "", errors.New("no secret configured; set secret_env or secret_file")
}

// reservedScopeName is the fixed key scope used by the unauthenticated
// /process endpoint. A consumer must not reuse it, otherwise its store key
// would alias the public scope.
const reservedScopeName = "process"

// validateConsumer checks the structural invariants of a consumer policy.
func validateConsumer(c Consumer) error {
	if c.Name == "" {
		return errors.New("consumer name must not be empty")
	}
	if c.Name == reservedScopeName {
		return fmt.Errorf("consumer name %q collides with reserved scope %q", c.Name, reservedScopeName)
	}
	if strings.Contains(c.Name, ":") {
		return fmt.Errorf("consumer name %q must not contain %q", c.Name, ":")
	}
	if len(c.Types) == 0 {
		return fmt.Errorf("consumer %q: at least one type is required", c.Name)
	}
	switch c.MaskFormat {
	case "marker", "stars":
	default:
		return fmt.Errorf("consumer %q: invalid mask format %q", c.Name, c.MaskFormat)
	}
	if c.Secret == "" {
		return fmt.Errorf("consumer %q: secret must not be empty", c.Name)
	}
	return nil
}

// validateRegexpRule checks the structural invariants of a regexp rule.
func validateRegexpRule(r RegexpRule) error {
	if r.Type == "" {
		return errors.New("regexp rule: type must not be empty")
	}
	if r.Pattern == "" {
		return fmt.Errorf("regexp rule %q: pattern must not be empty", r.Type)
	}
	if r.MaxMatches <= 0 {
		return fmt.Errorf("regexp rule %q: max matches must be positive", r.Type)
	}
	return nil
}
