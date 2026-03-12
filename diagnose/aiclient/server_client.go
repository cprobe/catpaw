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

	agentserver "github.com/cprobe/catpaw/server"
)

type GatewayClientConfig struct {
	BaseURL        string
	AgentToken     string
	Scene          string
	MaxTokens      int
	RequestTimeout time.Duration
}

type ServerClient struct {
	baseURL        string
	agentToken     string
	agentID        string
	scene          string
	httpClient     *http.Client
	maxTokens      int
	requestTimeout time.Duration
}

func NewServerClient(cfg GatewayClientConfig) (*ServerClient, error) {
	agentID, err := agentserver.LoadOrCreateAgentID()
	if err != nil {
		return nil, fmt.Errorf("load agent_id: %w", err)
	}
	return &ServerClient{
		baseURL:        strings.TrimRight(cfg.BaseURL, "/"),
		agentToken:     cfg.AgentToken,
		agentID:        agentID.String(),
		scene:          cfg.Scene,
		maxTokens:      cfg.MaxTokens,
		requestTimeout: cfg.RequestTimeout,
		httpClient:     &http.Client{Timeout: cfg.RequestTimeout},
	}, nil
}

func (c *ServerClient) Model() string {
	return "server:" + c.scene
}

func (c *ServerClient) Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResponse, error) {
	body, err := json.Marshal(GatewayChatRequest{
		Messages:  messages,
		Tools:     tools,
		MaxTokens: c.maxTokens,
		TimeoutMs: c.requestTimeout.Milliseconds(),
		Metadata:  gatewayMetadataFromContext(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", c.agentToken)
	req.Header.Set("X-Agent-ID", c.agentID)
	req.Header.Set("X-Agent-Scene", c.scene)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var env gatewayEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Body: msg}
	}
	if env.Data == nil {
		return nil, fmt.Errorf("unmarshal response: missing data")
	}

	return &ChatResponse{
		ID: env.Data.ID,
		Choices: []Choice{{
			Index:        0,
			Message:      env.Data.Message,
			FinishReason: env.Data.FinishReason,
		}},
		Usage: env.Data.Usage,
	}, nil
}
