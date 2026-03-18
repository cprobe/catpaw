package aiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cprobe/digcore/config"
)

func newTestServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

func okHandler(model string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ChatResponse{
			Choices: []Choice{{Message: Message{Role: "assistant", Content: "from:" + model}}},
			Usage:   Usage{TotalTokens: 10},
		})
	}
}

func failHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"error":{"message":"fail"}}`))
	}
}

func buildTestConfig(servers map[string]*httptest.Server, priority []string) config.AIConfig {
	models := make(map[string]config.ModelConfig, len(servers))
	for name, srv := range servers {
		models[name] = config.ModelConfig{
			BaseURL:   srv.URL,
			APIKey:    "k",
			Model:     name + "-model",
			MaxTokens: 100,
		}
	}
	return config.AIConfig{
		Enabled:        true,
		ModelPriority:  priority,
		Models:         models,
		MaxRetries:     0,
		RequestTimeout: config.Duration(5 * time.Second),
		RetryBackoff:   config.Duration(10 * time.Millisecond),
	}
}

func TestFailoverPrimarySuccess(t *testing.T) {
	srvA := newTestServer(okHandler("A"))
	defer srvA.Close()
	srvB := newTestServer(okHandler("B"))
	defer srvB.Close()

	fc := NewFailoverClient(buildTestConfig(
		map[string]*httptest.Server{"a": srvA, "b": srvB},
		[]string{"a", "b"},
	))

	resp, name, err := fc.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if name != "a" {
		t.Errorf("model = %q, want %q", name, "a")
	}
	if resp.Choices[0].Message.Content != "from:A" {
		t.Errorf("content = %q, want %q", resp.Choices[0].Message.Content, "from:A")
	}
}

func TestFailoverToSecondModel(t *testing.T) {
	srvA := newTestServer(failHandler(http.StatusInternalServerError))
	defer srvA.Close()
	srvB := newTestServer(okHandler("B"))
	defer srvB.Close()

	fc := NewFailoverClient(buildTestConfig(
		map[string]*httptest.Server{"a": srvA, "b": srvB},
		[]string{"a", "b"},
	))

	resp, name, err := fc.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if name != "b" {
		t.Errorf("model = %q, want %q", name, "b")
	}
	if resp.Choices[0].Message.Content != "from:B" {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
}

func TestFailoverNoFallbackOn4xx(t *testing.T) {
	srvA := newTestServer(failHandler(http.StatusUnauthorized))
	defer srvA.Close()
	srvB := newTestServer(okHandler("B"))
	defer srvB.Close()

	fc := NewFailoverClient(buildTestConfig(
		map[string]*httptest.Server{"a": srvA, "b": srvB},
		[]string{"a", "b"},
	))

	_, name, err := fc.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	if name != "a" {
		t.Errorf("model = %q, want %q (should not failover on 4xx)", name, "a")
	}
}

func TestFailoverAllFail(t *testing.T) {
	srvA := newTestServer(failHandler(http.StatusServiceUnavailable))
	defer srvA.Close()
	srvB := newTestServer(failHandler(http.StatusServiceUnavailable))
	defer srvB.Close()

	fc := NewFailoverClient(buildTestConfig(
		map[string]*httptest.Server{"a": srvA, "b": srvB},
		[]string{"a", "b"},
	))

	_, _, err := fc.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error when all models fail")
	}
}

func TestPinModel(t *testing.T) {
	srvA := newTestServer(okHandler("A"))
	defer srvA.Close()
	srvB := newTestServer(okHandler("B"))
	defer srvB.Close()

	fc := NewFailoverClient(buildTestConfig(
		map[string]*httptest.Server{"a": srvA, "b": srvB},
		[]string{"a", "b"},
	))

	if err := fc.PinModel("b"); err != nil {
		t.Fatalf("PinModel() error: %v", err)
	}

	_, name, err := fc.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if name != "b" {
		t.Errorf("model = %q, want %q (pinned)", name, "b")
	}

	if err := fc.PinModel(""); err != nil {
		t.Fatalf("PinModel('') error: %v", err)
	}
	_, name, _ = fc.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if name != "a" {
		t.Errorf("after unpin, model = %q, want %q", name, "a")
	}
}

func TestPinUnknownModel(t *testing.T) {
	fc := NewFailoverClient(config.AIConfig{
		ModelPriority: []string{"a"},
		Models: map[string]config.ModelConfig{
			"a": {BaseURL: "http://localhost", APIKey: "k"},
		},
	})
	if err := fc.PinModel("nonexistent"); err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
}

func TestFailover429TriggersFallback(t *testing.T) {
	srvA := newTestServer(failHandler(http.StatusTooManyRequests))
	defer srvA.Close()
	srvB := newTestServer(okHandler("B"))
	defer srvB.Close()

	fc := NewFailoverClient(buildTestConfig(
		map[string]*httptest.Server{"a": srvA, "b": srvB},
		[]string{"a", "b"},
	))

	_, name, err := fc.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if name != "b" {
		t.Errorf("model = %q, want %q (429 should trigger failover)", name, "b")
	}
}

func TestNewFailoverClientForSceneGateway(t *testing.T) {
	config.Config = &config.ConfigType{StateDir: t.TempDir()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(gatewayEnvelope{
			Data: &GatewayChatData{
				Message: Message{Role: "assistant", Content: "from gateway"},
			},
		})
	}))
	defer srv.Close()

	fc := NewFailoverClientForScene(config.AIConfig{
		Gateway: config.GatewayConfig{
			Enabled:        true,
			BaseURL:        srv.URL,
			AgentToken:     "token",
			RequestTimeout: config.Duration(time.Second),
			MaxRetries:     0,
		},
		RetryBackoff: config.Duration(10 * time.Millisecond),
	}, "chat")

	if !fc.IsGateway() {
		t.Fatal("expected gateway failover client")
	}
	if got := fc.ModelNames(); len(got) != 1 || got[0] != "server:chat" {
		t.Fatalf("ModelNames() = %v, want [server:chat]", got)
	}

	resp, name, err := fc.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if name != "server:chat" {
		t.Fatalf("model = %q, want server:chat", name)
	}
	if resp.Choices[0].Message.Content != "from gateway" {
		t.Fatalf("content = %q, want from gateway", resp.Choices[0].Message.Content)
	}
}

func TestNewFailoverClientForSceneGatewayInitFailure(t *testing.T) {
	config.Config = nil
	fc := NewFailoverClientForScene(config.AIConfig{
		Gateway: config.GatewayConfig{
			Enabled:        true,
			BaseURL:        "http://example.com",
			AgentToken:     "token",
			RequestTimeout: config.Duration(time.Second),
			MaxRetries:     0,
		},
		RetryBackoff: config.Duration(10 * time.Millisecond),
	}, "diagnose")

	_, _, err := fc.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected init error, got nil")
	}
	if !strings.Contains(err.Error(), "load agent_id") {
		t.Fatalf("unexpected error: %v", err)
	}
}
