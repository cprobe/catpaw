package aiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cprobe/catpaw/config"
)

func TestServerClientChat(t *testing.T) {
	stateDir := t.TempDir()
	config.Config = &config.ConfigType{StateDir: stateDir}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat" {
			t.Fatalf("path = %q, want /chat", r.URL.Path)
		}
		if got := r.Header.Get("X-Agent-Token"); got != "agent-token" {
			t.Fatalf("X-Agent-Token = %q, want agent-token", got)
		}
		if got := r.Header.Get("X-Agent-Scene"); got != "diagnose" {
			t.Fatalf("X-Agent-Scene = %q, want diagnose", got)
		}
		if got := r.Header.Get("X-Agent-ID"); got == "" {
			t.Fatal("X-Agent-ID is empty")
		}

		var req GatewayChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.TimeoutMs != int64((2 * time.Second).Milliseconds()) {
			t.Fatalf("timeout_ms = %d, want %d", req.TimeoutMs, (2 * time.Second).Milliseconds())
		}
		if req.Metadata.DiagnoseID != "diag-1" {
			t.Fatalf("metadata diagnose_id = %v, want diag-1", req.Metadata)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gatewayEnvelope{
			Data: &GatewayChatData{
				ID:           "resp-1",
				Message:      Message{Role: "assistant", Content: "gateway ok"},
				FinishReason: "stop",
				Usage:        Usage{TotalTokens: 12},
			},
		})
	}))
	defer srv.Close()

	client, err := NewServerClient(GatewayClientConfig{
		BaseURL:        srv.URL,
		AgentToken:     "agent-token",
		Scene:          "diagnose",
		RequestTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServerClient() error: %v", err)
	}

	resp, err := client.Chat(WithGatewayMetadata(context.Background(), GatewayMetadata{DiagnoseID: "diag-1"}), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.ID != "resp-1" {
		t.Fatalf("response id = %q, want resp-1", resp.ID)
	}
	if resp.Choices[0].Message.Content != "gateway ok" {
		t.Fatalf("content = %q, want gateway ok", resp.Choices[0].Message.Content)
	}

	agentIDPath := filepath.Join(stateDir, "agent_id")
	if _, err := os.Stat(agentIDPath); err != nil {
		t.Fatalf("agent_id file missing: %v", err)
	}
}

func TestServerClientChatError(t *testing.T) {
	config.Config = &config.ConfigType{StateDir: t.TempDir()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(gatewayEnvelope{Error: &gatewayErrorBody{Message: "upstream unavailable"}})
	}))
	defer srv.Close()

	client, err := NewServerClient(GatewayClientConfig{
		BaseURL:        srv.URL,
		AgentToken:     "agent-token",
		Scene:          "chat",
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewServerClient() error: %v", err)
	}

	_, err = client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", apiErr.StatusCode, http.StatusServiceUnavailable)
	}
}
