package config

import (
	"os"
	"testing"
)

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
