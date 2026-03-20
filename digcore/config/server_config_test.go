package config

import "testing"

func TestServerConfigDerivedURLs(t *testing.T) {
	tests := []struct {
		name         string
		address      string
		wantHTTPBase string
		wantWS       string
		wantLLM      string
	}{
		{
			name:         "host port without scheme",
			address:      "127.0.0.1:8080",
			wantHTTPBase: "http://127.0.0.1:8080",
			wantWS:       "ws://127.0.0.1:8080/api/v1/agent/ws",
			wantLLM:      "http://127.0.0.1:8080/api/v1/agent/llm",
		},
		{
			name:         "https address",
			address:      "https://brain.example.com",
			wantHTTPBase: "https://brain.example.com",
			wantWS:       "wss://brain.example.com/api/v1/agent/ws",
			wantLLM:      "https://brain.example.com/api/v1/agent/llm",
		},
		{
			name:         "ws address still accepted",
			address:      "ws://brain.example.com:8080",
			wantHTTPBase: "http://brain.example.com:8080",
			wantWS:       "ws://brain.example.com:8080/api/v1/agent/ws",
			wantLLM:      "http://brain.example.com:8080/api/v1/agent/llm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ServerConfig{Address: tt.address}

			httpBase, err := cfg.HTTPBaseURL()
			if err != nil {
				t.Fatalf("HTTPBaseURL() error = %v", err)
			}
			if httpBase != tt.wantHTTPBase {
				t.Fatalf("HTTPBaseURL() = %q, want %q", httpBase, tt.wantHTTPBase)
			}

			wsURL, err := cfg.WebSocketURL()
			if err != nil {
				t.Fatalf("WebSocketURL() error = %v", err)
			}
			if wsURL != tt.wantWS {
				t.Fatalf("WebSocketURL() = %q, want %q", wsURL, tt.wantWS)
			}

			llmURL, err := cfg.GatewayBaseURL()
			if err != nil {
				t.Fatalf("GatewayBaseURL() error = %v", err)
			}
			if llmURL != tt.wantLLM {
				t.Fatalf("GatewayBaseURL() = %q, want %q", llmURL, tt.wantLLM)
			}
		})
	}
}

func TestServerConfigRejectsPathInAddress(t *testing.T) {
	cfg := ServerConfig{Address: "http://127.0.0.1:8080/api/v1/agent/ws"}
	if _, err := cfg.WebSocketURL(); err == nil {
		t.Fatal("expected error, got nil")
	}
}
