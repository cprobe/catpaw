package config

import (
	"os"
	"testing"
	"time"
)

func TestAIConfigApplyDefaults(t *testing.T) {
	c := &AIConfig{Enabled: true}
	c.applyDefaults()

	if c.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", c.Model, "gpt-4o")
	}
	if c.MaxTokens != 4000 {
		t.Errorf("MaxTokens = %d, want 4000", c.MaxTokens)
	}
	if c.MaxRounds != 8 {
		t.Errorf("MaxRounds = %d, want 8", c.MaxRounds)
	}
	if time.Duration(c.RequestTimeout) != 60*time.Second {
		t.Errorf("RequestTimeout = %v, want 60s", time.Duration(c.RequestTimeout))
	}
	if c.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2", c.MaxRetries)
	}
	if c.MaxConcurrentDiagnoses != 3 {
		t.Errorf("MaxConcurrentDiagnoses = %d, want 3", c.MaxConcurrentDiagnoses)
	}
	if c.QueueFullPolicy != "drop" {
		t.Errorf("QueueFullPolicy = %q, want %q", c.QueueFullPolicy, "drop")
	}
	if time.Duration(c.ToolTimeout) != 10*time.Second {
		t.Errorf("ToolTimeout = %v, want 10s", time.Duration(c.ToolTimeout))
	}
	if time.Duration(c.AggregateWindow) != 5*time.Second {
		t.Errorf("AggregateWindow = %v, want 5s", time.Duration(c.AggregateWindow))
	}
	if c.DiagnoseMaxCount != 1000 {
		t.Errorf("DiagnoseMaxCount = %d, want 1000", c.DiagnoseMaxCount)
	}
}

func TestAIConfigApplyDefaultsDisabled(t *testing.T) {
	c := &AIConfig{Enabled: false}
	c.applyDefaults()

	if c.MaxRetries != 0 {
		t.Errorf("MaxRetries = %d, want 0 when disabled", c.MaxRetries)
	}
}

func TestAIConfigApplyDefaultsPreserve(t *testing.T) {
	c := &AIConfig{
		Enabled:  true,
		Model:    "deepseek-v3",
		MaxRounds: 5,
	}
	c.applyDefaults()

	if c.Model != "deepseek-v3" {
		t.Errorf("Model = %q, want %q (should preserve)", c.Model, "deepseek-v3")
	}
	if c.MaxRounds != 5 {
		t.Errorf("MaxRounds = %d, want 5 (should preserve)", c.MaxRounds)
	}
}

func TestAIConfigResolveAPIKey(t *testing.T) {
	t.Run("env var reference", func(t *testing.T) {
		os.Setenv("TEST_AI_KEY", "sk-test-12345")
		defer os.Unsetenv("TEST_AI_KEY")

		c := &AIConfig{APIKey: "${TEST_AI_KEY}"}
		c.resolveAPIKey()

		if c.APIKey != "sk-test-12345" {
			t.Errorf("APIKey = %q, want %q", c.APIKey, "sk-test-12345")
		}
	})

	t.Run("literal key unchanged", func(t *testing.T) {
		c := &AIConfig{APIKey: "sk-literal-key"}
		c.resolveAPIKey()

		if c.APIKey != "sk-literal-key" {
			t.Errorf("APIKey = %q, want %q", c.APIKey, "sk-literal-key")
		}
	})

	t.Run("empty env var", func(t *testing.T) {
		os.Unsetenv("NONEXISTENT_KEY")
		c := &AIConfig{APIKey: "${NONEXISTENT_KEY}"}
		c.resolveAPIKey()

		if c.APIKey != "" {
			t.Errorf("APIKey = %q, want empty", c.APIKey)
		}
	})
}

func TestAIConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     AIConfig
		wantErr bool
	}{
		{
			name:    "disabled is always valid",
			cfg:     AIConfig{Enabled: false},
			wantErr: false,
		},
		{
			name:    "enabled without base_url",
			cfg:     AIConfig{Enabled: true, APIKey: "sk-123"},
			wantErr: true,
		},
		{
			name:    "enabled without api_key",
			cfg:     AIConfig{Enabled: true, BaseURL: "http://localhost"},
			wantErr: true,
		},
		{
			name:    "invalid queue_full_policy",
			cfg:     AIConfig{Enabled: true, BaseURL: "http://localhost", APIKey: "sk-123", QueueFullPolicy: "invalid"},
			wantErr: true,
		},
		{
			name:    "valid config with drop policy",
			cfg:     AIConfig{Enabled: true, BaseURL: "http://localhost", APIKey: "sk-123", QueueFullPolicy: "drop"},
			wantErr: false,
		},
		{
			name:    "valid config with wait policy",
			cfg:     AIConfig{Enabled: true, BaseURL: "http://localhost", APIKey: "sk-123", QueueFullPolicy: "wait"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
