package aiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client communicates with an OpenAI-compatible /v1/chat/completions endpoint.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	maxTokens  int
	httpClient *http.Client
}

// ClientConfig holds the parameters needed to create a Client.
type ClientConfig struct {
	BaseURL        string
	APIKey         string
	Model          string
	MaxTokens      int
	RequestTimeout time.Duration
}

// NewClient creates a new AI API client.
func NewClient(cfg ClientConfig) *Client {
	return &Client{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		httpClient: &http.Client{
			Timeout: cfg.RequestTimeout,
		},
	}
}

// Chat sends a single chat completion request and returns the parsed response.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResponse, error) {
	reqBody := ChatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
	}
	if c.maxTokens > 0 {
		reqBody.MaxTokens = c.maxTokens
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w (body: %s)", err, truncBody(body))
	}

	return &chatResp, nil
}

// APIError represents a non-200 response from the AI API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("AI API error (status %d): %s", e.StatusCode, truncStr(e.Body, 200))
}

func truncBody(b []byte) string {
	return truncStr(string(b), 200)
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
