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
	"unicode/utf8"
)

// Client communicates with an OpenAI-compatible /v1/chat/completions endpoint.
type Client struct {
	baseURL             string
	apiKey              string
	model               string
	maxTokens           int
	maxCompletionTokens int
	extraBody           map[string]interface{}
	httpClient          *http.Client
}

// ClientConfig holds the parameters needed to create a Client.
type ClientConfig struct {
	BaseURL             string
	APIKey              string
	Model               string
	MaxTokens           int
	MaxCompletionTokens int
	RequestTimeout      time.Duration
	ExtraBody           map[string]interface{}
}

// NewClient creates a new AI API client.
func NewClient(cfg ClientConfig) *Client {
	return &Client{
		baseURL:             strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:              cfg.APIKey,
		model:               cfg.Model,
		maxTokens:           cfg.MaxTokens,
		maxCompletionTokens: cfg.MaxCompletionTokens,
		extraBody:           cfg.ExtraBody,
		httpClient: &http.Client{
			Timeout: cfg.RequestTimeout,
		},
	}
}

// Model returns the model name configured for this client.
func (c *Client) Model() string { return c.model }

// Chat sends a single chat completion request and returns the parsed response.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResponse, error) {
	payload, err := c.buildRequestPayload(messages, tools)
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

	const maxResponseBody = 10 << 20 // 10 MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       truncStr(string(body), 1024),
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

func truncStr(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes] + "..."
}

// buildRequestPayload constructs the JSON body for a chat completion request.
// Vendor-specific extraBody fields are merged first, then standard fields are
// set on top so they can never be accidentally overridden.
func (c *Client) buildRequestPayload(messages []Message, tools []Tool) ([]byte, error) {
	body := make(map[string]interface{}, 4+len(c.extraBody))

	for k, v := range c.extraBody {
		body[k] = v
	}

	body["model"] = c.model
	body["messages"] = messages
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if c.maxCompletionTokens > 0 {
		delete(body, "max_tokens")
		body["max_completion_tokens"] = c.maxCompletionTokens
	} else if c.maxTokens > 0 {
		delete(body, "max_completion_tokens")
		body["max_tokens"] = c.maxTokens
	}

	return json.Marshal(body)
}
