package mcp

import (
	"encoding/json"
	"testing"
)

func TestExtractParams_ValidSchema(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "PromQL expression"},
			"start": {"type": "string", "description": "Start time"},
			"step":  {"type": "string", "description": "Step duration"}
		},
		"required": ["query"]
	}`
	tool := Tool{
		Name:        "query_range",
		Description: "Query Prometheus",
		InputSchema: json.RawMessage(schema),
	}

	params := tool.ExtractParams()
	if len(params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(params))
	}

	byName := make(map[string]ToolParam)
	for _, p := range params {
		byName[p.Name] = p
	}

	q, ok := byName["query"]
	if !ok {
		t.Fatal("missing query param")
	}
	if !q.Required {
		t.Error("query should be required")
	}
	if q.Type != "string" {
		t.Errorf("query type should be string, got %q", q.Type)
	}

	s, ok := byName["start"]
	if !ok {
		t.Fatal("missing start param")
	}
	if s.Required {
		t.Error("start should not be required")
	}
}

func TestExtractParams_EmptySchema(t *testing.T) {
	tool := Tool{Name: "no_params"}
	params := tool.ExtractParams()
	if params != nil {
		t.Errorf("expected nil, got %v", params)
	}
}

func TestExtractParams_InvalidJSON(t *testing.T) {
	tool := Tool{
		Name:        "broken",
		InputSchema: json.RawMessage(`not json`),
	}
	params := tool.ExtractParams()
	if params != nil {
		t.Errorf("expected nil for invalid JSON, got %v", params)
	}
}

func TestExtractTexts(t *testing.T) {
	contents := []ToolContent{
		{Type: "text", Text: "line1"},
		{Type: "image", Text: "binary"},
		{Type: "text", Text: "line2"},
		{Type: "text", Text: ""},
	}
	got := extractTexts(contents)
	if got != "line1\nline2" {
		t.Errorf("expected 'line1\\nline2', got %q", got)
	}
}

func TestTruncRaw(t *testing.T) {
	short := "hello"
	if truncRaw([]byte(short)) != "hello" {
		t.Error("short string should not be truncated")
	}

	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	result := truncRaw(long)
	if len(result) != 203 {
		t.Errorf("expected length 203, got %d", len(result))
	}
}
