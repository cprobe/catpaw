package aiclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
)

// FailoverClient wraps multiple AI clients and routes requests according to
// a priority list. If the current client returns a failover-eligible error,
// the next client in line is tried.
type FailoverClient struct {
	mu       sync.RWMutex
	priority []string
	clients  map[string]ChatClient
	retryCfg RetryConfig
	pinned   string // when non-empty, only this model is used (no failover)
}

// NewFailoverClient builds a FailoverClient from the AIConfig.
// It creates one ChatClient per entry in ModelPriority, selecting
// the appropriate backend (OpenAI or Bedrock) based on the provider field.
func NewFailoverClient(cfg config.AIConfig) *FailoverClient {
	clients := make(map[string]ChatClient, len(cfg.ModelPriority))
	for _, name := range cfg.ModelPriority {
		m := cfg.Models[name]
		clients[name] = newChatClient(m, time.Duration(cfg.RequestTimeout))
	}
	return &FailoverClient{
		priority: cfg.ModelPriority,
		clients:  clients,
		retryCfg: RetryConfig{
			MaxRetries:   cfg.MaxRetries,
			RetryBackoff: time.Duration(cfg.RetryBackoff),
		},
	}
}

// newChatClient creates the appropriate ChatClient based on the provider field.
func newChatClient(m config.ModelConfig, timeout time.Duration) ChatClient {
	if m.Provider == "bedrock" {
		return NewBedrockClient(BedrockClientConfig{
			Region:         m.ExtraStr("aws_region"),
			Model:          m.Model,
			MaxTokens:      m.MaxTokens,
			RequestTimeout: timeout,
			Credentials: AWSCredentials{
				AccessKeyID:     m.ExtraStr("aws_access_key_id"),
				SecretAccessKey: m.ExtraStr("aws_secret_access_key"),
				SessionToken:    m.ExtraStr("aws_session_token"),
			},
		})
	}
	return NewClient(ClientConfig{
		BaseURL:        m.BaseURL,
		APIKey:         m.APIKey,
		AuthHeader:     m.AuthHeader,
		Model:          m.Model,
		MaxTokens:      m.MaxTokens,
		RequestTimeout: timeout,
		ExtraBody:      m.ExtraBody,
	})
}

// Chat sends a request, trying models in priority order (or only the pinned
// model). Returns the response, the name of the model that answered, and any
// error. Each model attempt uses ChatWithRetry internally.
func (fc *FailoverClient) Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResponse, string, error) {
	fc.mu.RLock()
	pinned := fc.pinned
	fc.mu.RUnlock()

	if pinned != "" {
		c, ok := fc.clients[pinned]
		if !ok {
			return nil, pinned, fmt.Errorf("pinned model %q not found", pinned)
		}
		resp, err := ChatWithRetry(ctx, c, fc.retryCfg, messages, tools)
		return resp, pinned, err
	}

	var lastErr error
	for _, name := range fc.priority {
		c := fc.clients[name]
		resp, err := ChatWithRetry(ctx, c, fc.retryCfg, messages, tools)
		if err == nil {
			return resp, name, nil
		}
		if !shouldFailover(err) {
			return nil, name, err
		}
		lastErr = fmt.Errorf("model %s: %w", name, err)
	}
	return nil, "", fmt.Errorf("all %d models failed, last: %w", len(fc.priority), lastErr)
}

// PinModel locks the client to use only the named model (no failover).
// Pass an empty string to unpin (restore failover behavior).
func (fc *FailoverClient) PinModel(name string) error {
	if name != "" {
		if _, ok := fc.clients[name]; !ok {
			return fmt.Errorf("unknown model %q", name)
		}
	}
	fc.mu.Lock()
	fc.pinned = name
	fc.mu.Unlock()
	return nil
}

// PinnedModel returns the currently pinned model name, or "" if using failover.
func (fc *FailoverClient) PinnedModel() string {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.pinned
}

// ModelNames returns a copy of the priority-ordered list of model names.
func (fc *FailoverClient) ModelNames() []string {
	out := make([]string, len(fc.priority))
	copy(out, fc.priority)
	return out
}

// ModelClient returns the underlying ChatClient for a specific model name.
func (fc *FailoverClient) ModelClient(name string) (ChatClient, bool) {
	c, ok := fc.clients[name]
	return c, ok
}

// shouldFailover decides whether to try the next model after an error.
// 5xx and 429 (transient server issues) trigger failover.
// 4xx (client errors like bad request or auth failure) do not.
func shouldFailover(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500 || apiErr.StatusCode == 429
	}
	return true
}
