package mcp

import "encoding/json"

// --- JSON-RPC 2.0 ---

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonrpcError) Error() string {
	return e.Message
}

// --- MCP Protocol Types ---

const protocolVersion = "2024-11-05"

type initializeParams struct {
	ProtocolVersion string           `json:"protocolVersion"`
	Capabilities    clientCaps       `json:"capabilities"`
	ClientInfo      implementationID `json:"clientInfo"`
}

type clientCaps struct{}

type implementationID struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type initializeResult struct {
	ProtocolVersion string           `json:"protocolVersion"`
	ServerInfo      implementationID `json:"serverInfo"`
}

// Tool describes one tool exposed by an MCP server.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolContent represents one content block in a tool call response.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type toolCallResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolParam extracts parameter info from a tool's JSON Schema inputSchema.
type ToolParam struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

// ExtractParams parses the inputSchema JSON Schema into a flat list of ToolParams.
func (t *Tool) ExtractParams() []ToolParam {
	if len(t.InputSchema) == 0 {
		return nil
	}

	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
		return nil
	}

	reqSet := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		reqSet[r] = true
	}

	params := make([]ToolParam, 0, len(schema.Properties))
	for name, prop := range schema.Properties {
		params = append(params, ToolParam{
			Name:        name,
			Type:        prop.Type,
			Description: prop.Description,
			Required:    reqSet[name],
		})
	}
	return params
}
