package aiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
		}

		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "gpt-4o" {
			t.Errorf("model = %q, want gpt-4o", req.Model)
		}

		resp := ChatResponse{
			ID: "chatcmpl-123",
			Choices: []Choice{{
				Index:        0,
				Message:      Message{Role: "assistant", Content: "hello"},
				FinishReason: "stop",
			}},
			Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(ClientConfig{
		BaseURL:        srv.URL,
		APIKey:         "test-key",
		Model:          "gpt-4o",
		MaxTokens:      100,
		RequestTimeout: 5 * time.Second,
	})

	resp, err := client.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("len(Choices) = %d, want 1", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "hello" {
		t.Errorf("Content = %q, want %q", resp.Choices[0].Message.Content, "hello")
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestChatWithRetryRecovers(t *testing.T) {
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		resp := ChatResponse{
			Choices: []Choice{{Message: Message{Role: "assistant", Content: "ok"}}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(ClientConfig{
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "m",
		RequestTimeout: 5 * time.Second,
	})

	resp, err := ChatWithRetry(context.Background(), client,
		RetryConfig{MaxRetries: 3, RetryBackoff: 10 * time.Millisecond},
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("ChatWithRetry() error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("Content = %q, want %q", resp.Choices[0].Message.Content, "ok")
	}
	if attempt != 3 {
		t.Errorf("attempts = %d, want 3", attempt)
	}
}

func TestChatExtraBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if raw["enable_thinking"] != true {
			t.Errorf("enable_thinking = %v, want true", raw["enable_thinking"])
		}
		if raw["thinking_budget"] != float64(10000) {
			t.Errorf("thinking_budget = %v, want 10000", raw["thinking_budget"])
		}
		if raw["model"] != "deepseek-reasoner" {
			t.Errorf("model = %v, want deepseek-reasoner (standard fields must override extra_body)", raw["model"])
		}

		resp := ChatResponse{
			Choices: []Choice{{Message: Message{Role: "assistant", Content: "ok"}}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(ClientConfig{
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "deepseek-reasoner",
		MaxTokens:      8000,
		RequestTimeout: 5 * time.Second,
		ExtraBody: map[string]interface{}{
			"enable_thinking": true,
			"thinking_budget": 10000,
			"model":           "should-be-overridden",
		},
	})

	resp, err := client.Chat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("Content = %q, want %q", resp.Choices[0].Message.Content, "ok")
	}
}

func TestClientModel(t *testing.T) {
	c := NewClient(ClientConfig{Model: "test-model"})
	if c.Model() != "test-model" {
		t.Errorf("Model() = %q, want %q", c.Model(), "test-model")
	}
}

func TestChatNonRetryableError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()

	client := NewClient(ClientConfig{
		BaseURL:        srv.URL,
		APIKey:         "bad",
		Model:          "m",
		RequestTimeout: 5 * time.Second,
	})

	_, err := ChatWithRetry(context.Background(), client,
		RetryConfig{MaxRetries: 3, RetryBackoff: 10 * time.Millisecond},
		[]Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
