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

// BedrockClient communicates with the AWS Bedrock Converse API using SigV4.
type BedrockClient struct {
	region     string
	model      string // inference profile ID, e.g. "us.anthropic.claude-sonnet-4-6"
	maxTokens  int
	creds      AWSCredentials
	httpClient *http.Client
}

// BedrockClientConfig holds the parameters needed to create a BedrockClient.
type BedrockClientConfig struct {
	Region         string
	Model          string
	MaxTokens      int
	RequestTimeout time.Duration
	Credentials    AWSCredentials
}

// NewBedrockClient creates a new Bedrock Converse API client.
func NewBedrockClient(cfg BedrockClientConfig) *BedrockClient {
	return &BedrockClient{
		region:    cfg.Region,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
		creds:     cfg.Credentials,
		httpClient: &http.Client{
			Timeout: cfg.RequestTimeout,
		},
	}
}

// Model returns the model identifier.
func (b *BedrockClient) Model() string { return b.model }

// Chat sends a Converse request to Bedrock and returns a ChatResponse
// compatible with the OpenAI format used by the rest of catpaw.
func (b *BedrockClient) Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResponse, error) {
	converseReq := b.buildConverseRequest(messages, tools)
	payload, err := json.Marshal(converseReq)
	if err != nil {
		return nil, fmt.Errorf("marshal bedrock request: %w", err)
	}

	url := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/converse",
		b.region, b.model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	payloadHash := hashSHA256(payload)
	signV4(httpReq, payloadHash, b.region, "bedrock", b.creds, time.Now())

	resp, err := b.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	const maxBody = 10 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       truncStr(string(body), 1024),
		}
	}

	var converseResp bedrockConverseResponse
	if err := json.Unmarshal(body, &converseResp); err != nil {
		return nil, fmt.Errorf("unmarshal bedrock response: %w (body: %s)", err, truncStr(string(body), 200))
	}

	return b.toChatResponse(&converseResp), nil
}

// --- Bedrock Converse request/response types ---

type bedrockConverseRequest struct {
	System          []bedrockContentBlock `json:"system,omitempty"`
	Messages        []bedrockMessage      `json:"messages"`
	InferenceConfig *bedrockInferConfig   `json:"inferenceConfig,omitempty"`
	ToolConfig      *bedrockToolConfig    `json:"toolConfig,omitempty"`
}

type bedrockMessage struct {
	Role    string                `json:"role"`
	Content []bedrockContentBlock `json:"content"`
}

type bedrockContentBlock struct {
	Text       string              `json:"text,omitempty"`
	ToolUse    *bedrockToolUse     `json:"toolUse,omitempty"`
	ToolResult *bedrockToolResult  `json:"toolResult,omitempty"`
}

type bedrockToolUse struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type bedrockToolResult struct {
	ToolUseID string                `json:"toolUseId"`
	Content   []bedrockContentBlock `json:"content"`
}

type bedrockInferConfig struct {
	MaxTokens int `json:"maxTokens,omitempty"`
}

type bedrockToolConfig struct {
	Tools []bedrockToolDef `json:"tools"`
}

type bedrockToolDef struct {
	ToolSpec *bedrockToolSpec `json:"toolSpec"`
}

type bedrockToolSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type bedrockInputSchema struct {
	JSON interface{} `json:"json"`
}

type bedrockConverseResponse struct {
	Output *bedrockOutput `json:"output"`
	StopReason string    `json:"stopReason"`
	Usage  *bedrockUsage  `json:"usage"`
}

type bedrockOutput struct {
	Message *bedrockMessage `json:"message"`
}

type bedrockUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

// --- conversion logic ---

// buildConverseRequest converts OpenAI-style messages+tools into Bedrock Converse format.
func (b *BedrockClient) buildConverseRequest(messages []Message, tools []Tool) *bedrockConverseRequest {
	req := &bedrockConverseRequest{
		InferenceConfig: &bedrockInferConfig{MaxTokens: b.maxTokens},
	}

	// Extract system messages, convert the rest.
	for _, msg := range messages {
		if msg.Role == "system" {
			req.System = append(req.System, bedrockContentBlock{Text: msg.Content})
			continue
		}
		req.Messages = append(req.Messages, b.convertMessage(msg))
	}

	// Merge consecutive same-role messages (Bedrock requires strict alternation).
	req.Messages = mergeConsecutiveMessages(req.Messages)

	if len(tools) > 0 {
		req.ToolConfig = b.convertTools(tools)
	}

	return req
}

// convertMessage converts a single OpenAI message to Bedrock format.
func (b *BedrockClient) convertMessage(msg Message) bedrockMessage {
	switch msg.Role {
	case "assistant":
		return b.convertAssistantMessage(msg)
	case "tool":
		return b.convertToolResultMessage(msg)
	default: // "user"
		return bedrockMessage{
			Role:    "user",
			Content: []bedrockContentBlock{{Text: msg.Content}},
		}
	}
}

func (b *BedrockClient) convertAssistantMessage(msg Message) bedrockMessage {
	var blocks []bedrockContentBlock
	if msg.Content != "" {
		blocks = append(blocks, bedrockContentBlock{Text: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		inputRaw := json.RawMessage(tc.Function.Arguments)
		// Validate it's valid JSON; if not, wrap as string.
		if !json.Valid(inputRaw) {
			inputRaw, _ = json.Marshal(tc.Function.Arguments)
		}
		blocks = append(blocks, bedrockContentBlock{
			ToolUse: &bedrockToolUse{
				ToolUseID: tc.ID,
				Name:      tc.Function.Name,
				Input:     inputRaw,
			},
		})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, bedrockContentBlock{Text: ""})
	}
	return bedrockMessage{Role: "assistant", Content: blocks}
}

// convertToolResultMessage converts an OpenAI "tool" role message to a Bedrock
// "user" role message with a toolResult content block.
func (b *BedrockClient) convertToolResultMessage(msg Message) bedrockMessage {
	return bedrockMessage{
		Role: "user",
		Content: []bedrockContentBlock{
			{
				ToolResult: &bedrockToolResult{
					ToolUseID: msg.ToolCallID,
					Content:   []bedrockContentBlock{{Text: msg.Content}},
				},
			},
		},
	}
}

// mergeConsecutiveMessages merges consecutive messages with the same role.
// Bedrock Converse requires strict user/assistant alternation.
func mergeConsecutiveMessages(msgs []bedrockMessage) []bedrockMessage {
	if len(msgs) == 0 {
		return msgs
	}
	merged := []bedrockMessage{msgs[0]}
	for i := 1; i < len(msgs); i++ {
		last := &merged[len(merged)-1]
		if msgs[i].Role == last.Role {
			last.Content = append(last.Content, msgs[i].Content...)
		} else {
			merged = append(merged, msgs[i])
		}
	}
	return merged
}

func (b *BedrockClient) convertTools(tools []Tool) *bedrockToolConfig {
	defs := make([]bedrockToolDef, 0, len(tools))
	for _, t := range tools {
		var schema interface{} = bedrockInputSchema{JSON: t.Function.Parameters}
		if t.Function.Parameters == nil {
			schema = bedrockInputSchema{JSON: map[string]interface{}{"type": "object"}}
		}
		defs = append(defs, bedrockToolDef{
			ToolSpec: &bedrockToolSpec{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: schema,
			},
		})
	}
	return &bedrockToolConfig{Tools: defs}
}

// toChatResponse converts a Bedrock Converse response back to the OpenAI
// ChatResponse format expected by catpaw's diagnose engine and chat REPL.
func (b *BedrockClient) toChatResponse(resp *bedrockConverseResponse) *ChatResponse {
	msg := Message{Role: "assistant"}

	if resp.Output != nil && resp.Output.Message != nil {
		var textParts []string
		for _, block := range resp.Output.Message.Content {
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
			if block.ToolUse != nil {
				argsStr := string(block.ToolUse.Input)
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{
					ID:   block.ToolUse.ToolUseID,
					Type: "function",
					Function: FunctionCall{
						Name:      block.ToolUse.Name,
						Arguments: argsStr,
					},
				})
			}
		}
		msg.Content = strings.Join(textParts, "\n")
	}

	finishReason := "stop"
	if resp.StopReason == "tool_use" {
		finishReason = "tool_calls"
	} else if resp.StopReason == "max_tokens" {
		finishReason = "length"
	}

	chatResp := &ChatResponse{
		Choices: []Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishReason,
		}},
	}

	if resp.Usage != nil {
		chatResp.Usage = Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
		if chatResp.Usage.TotalTokens == 0 {
			chatResp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
		}
	}

	return chatResp
}
