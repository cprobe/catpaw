package config

import (
	"os"
	"testing"
)

func TestResolveAPIKeys(t *testing.T) {
	t.Run("env var reference", func(t *testing.T) {
		os.Setenv("TEST_AI_KEY", "sk-test-12345")
		defer os.Unsetenv("TEST_AI_KEY")

		c := &AIConfig{Models: map[string]ModelConfig{
			"m1": {APIKey: "${TEST_AI_KEY}"},
		}}
		c.resolveAPIKeys()

		if c.Models["m1"].APIKey != "sk-test-12345" {
			t.Errorf("APIKey = %q, want %q", c.Models["m1"].APIKey, "sk-test-12345")
		}
	})

	t.Run("literal key unchanged", func(t *testing.T) {
		c := &AIConfig{Models: map[string]ModelConfig{
			"m1": {APIKey: "sk-literal-key"},
		}}
		c.resolveAPIKeys()

		if c.Models["m1"].APIKey != "sk-literal-key" {
			t.Errorf("APIKey = %q, want %q", c.Models["m1"].APIKey, "sk-literal-key")
		}
	})

	t.Run("empty env var", func(t *testing.T) {
		os.Unsetenv("NONEXISTENT_KEY")
		c := &AIConfig{Models: map[string]ModelConfig{
			"m1": {APIKey: "${NONEXISTENT_KEY}"},
		}}
		c.resolveAPIKeys()

		if c.Models["m1"].APIKey != "" {
			t.Errorf("APIKey = %q, want empty", c.Models["m1"].APIKey)
		}
	})

	t.Run("multiple models resolved independently", func(t *testing.T) {
		os.Setenv("KEY_A", "val-a")
		os.Setenv("KEY_B", "val-b")
		defer os.Unsetenv("KEY_A")
		defer os.Unsetenv("KEY_B")

		c := &AIConfig{Models: map[string]ModelConfig{
			"a": {APIKey: "${KEY_A}"},
			"b": {APIKey: "${KEY_B}"},
			"c": {APIKey: "literal"},
		}}
		c.resolveAPIKeys()

		if c.Models["a"].APIKey != "val-a" {
			t.Errorf("a.APIKey = %q, want %q", c.Models["a"].APIKey, "val-a")
		}
		if c.Models["b"].APIKey != "val-b" {
			t.Errorf("b.APIKey = %q, want %q", c.Models["b"].APIKey, "val-b")
		}
		if c.Models["c"].APIKey != "literal" {
			t.Errorf("c.APIKey = %q, want %q", c.Models["c"].APIKey, "literal")
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
			name: "enabled without model_priority",
			cfg: AIConfig{
				Enabled: true,
				Models:  map[string]ModelConfig{"m": {BaseURL: "http://x", APIKey: "k"}},
			},
			wantErr: true,
		},
		{
			name: "enabled without models",
			cfg: AIConfig{
				Enabled:       true,
				ModelPriority: []string{"m"},
			},
			wantErr: true,
		},
		{
			name: "priority references unknown model",
			cfg: AIConfig{
				Enabled:       true,
				ModelPriority: []string{"unknown"},
				Models:        map[string]ModelConfig{"m": {BaseURL: "http://x", APIKey: "k"}},
			},
			wantErr: true,
		},
		{
			name: "model missing base_url",
			cfg: AIConfig{
				Enabled:       true,
				ModelPriority: []string{"m"},
				Models:        map[string]ModelConfig{"m": {APIKey: "k"}},
			},
			wantErr: true,
		},
		{
			name: "model missing api_key",
			cfg: AIConfig{
				Enabled:       true,
				ModelPriority: []string{"m"},
				Models:        map[string]ModelConfig{"m": {BaseURL: "http://x"}},
			},
			wantErr: true,
		},
		{
			name: "invalid queue_full_policy",
			cfg: AIConfig{
				Enabled:         true,
				ModelPriority:   []string{"m"},
				Models:          map[string]ModelConfig{"m": {BaseURL: "http://x", APIKey: "k"}},
				QueueFullPolicy: "invalid",
			},
			wantErr: true,
		},
		{
			name: "valid config with drop policy",
			cfg: AIConfig{
				Enabled:         true,
				ModelPriority:   []string{"m"},
				Models:          map[string]ModelConfig{"m": {BaseURL: "http://x", APIKey: "k"}},
				QueueFullPolicy: "drop",
			},
			wantErr: false,
		},
		{
			name: "valid multi-model config",
			cfg: AIConfig{
				Enabled:         true,
				ModelPriority:   []string{"a", "b"},
				Models:          map[string]ModelConfig{"a": {BaseURL: "http://x", APIKey: "k1"}, "b": {BaseURL: "http://y", APIKey: "k2"}},
				QueueFullPolicy: "wait",
			},
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

func TestAIConfigApplyDefaults(t *testing.T) {
	c := &AIConfig{
		Models: map[string]ModelConfig{
			"m": {BaseURL: "http://x", APIKey: "k"},
		},
	}
	c.applyDefaults()

	if c.MaxRounds != 15 {
		t.Errorf("MaxRounds = %d, want 15", c.MaxRounds)
	}
	if c.Language != "zh" {
		t.Errorf("Language = %q, want %q", c.Language, "zh")
	}

	m := c.Models["m"]
	if m.MaxTokens != 4000 {
		t.Errorf("m.MaxTokens = %d, want 4000", m.MaxTokens)
	}
	if m.ContextWindow != 128000 {
		t.Errorf("m.ContextWindow = %d, want 128000", m.ContextWindow)
	}
}

func TestAIConfigPrimaryModel(t *testing.T) {
	c := &AIConfig{
		ModelPriority: []string{"fast", "smart"},
		Models: map[string]ModelConfig{
			"fast":  {Model: "gpt-4o-mini"},
			"smart": {Model: "gpt-4o"},
		},
	}
	if c.PrimaryModel().Model != "gpt-4o-mini" {
		t.Errorf("PrimaryModel().Model = %q, want %q", c.PrimaryModel().Model, "gpt-4o-mini")
	}
	if c.PrimaryModelName() != "fast" {
		t.Errorf("PrimaryModelName() = %q, want %q", c.PrimaryModelName(), "fast")
	}
}
