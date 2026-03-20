package aiclient

import "context"

type GatewayMetadata struct {
	DiagnoseID    string `json:"diagnose_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	MachineID     string `json:"machine_id,omitempty"`
	Plugin        string `json:"plugin,omitempty"`
	Target        string `json:"target,omitempty"`
	RequestSource string `json:"request_source,omitempty"`
}

type GatewayChatRequest struct {
	Messages  []Message       `json:"messages"`
	Tools     []Tool          `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	TimeoutMs int64           `json:"timeout_ms,omitempty"`
	Metadata  GatewayMetadata `json:"metadata,omitempty"`
}

type GatewayChatData struct {
	ID                string  `json:"id"`
	Model             string  `json:"model"`
	Message           Message `json:"message"`
	FinishReason      string  `json:"finish_reason"`
	Usage             Usage   `json:"usage"`
	ProviderLatencyMs int64   `json:"provider_latency_ms"`
	Attempts          int     `json:"attempts"`
}

type gatewayEnvelope struct {
	RequestID string            `json:"request_id"`
	Data      *GatewayChatData  `json:"data,omitempty"`
	Error     *gatewayErrorBody `json:"error,omitempty"`
}

type gatewayErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type gatewayMetadataKey struct{}

func WithGatewayMetadata(ctx context.Context, metadata GatewayMetadata) context.Context {
	return context.WithValue(ctx, gatewayMetadataKey{}, metadata)
}

func gatewayMetadataFromContext(ctx context.Context) GatewayMetadata {
	if ctx == nil {
		return GatewayMetadata{}
	}
	metadata, _ := ctx.Value(gatewayMetadataKey{}).(GatewayMetadata)
	return metadata
}
