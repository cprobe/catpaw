package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/diagnose/aiclient"
)

func msg(role string) aiclient.Message { return aiclient.Message{Role: role} }

func msgWithToolCalls(role string) aiclient.Message {
	return aiclient.Message{
		Role:      role,
		ToolCalls: []aiclient.ToolCall{{ID: "tc1", Function: aiclient.FunctionCall{Name: "f"}}},
	}
}

type testChatIO struct{}

func (testChatIO) OnThinkingStart(int) {}
func (testChatIO) OnThinkingDone(int, time.Duration) {}
func (testChatIO) OnReasoning(string) {}
func (testChatIO) OnToolStart(string, string) {}
func (testChatIO) OnToolDone(string, string, time.Duration, int, bool) {}
func (testChatIO) OnToolOutput(string) {}
func (testChatIO) ApproveShell(string) (bool, string) { return false, "" }

func TestTrimHistory_BelowLimit(t *testing.T) {
	msgs := make([]aiclient.Message, maxHistoryMessages+1)
	msgs[0] = msg("system")
	for i := 1; i < len(msgs); i++ {
		msgs[i] = msg("user")
	}
	result := trimHistory(msgs)
	if len(result) != len(msgs) {
		t.Fatalf("expected no trim, got %d messages (was %d)", len(result), len(msgs))
	}
}

func TestTrimHistory_ExactlyOverLimit(t *testing.T) {
	n := maxHistoryMessages + 5
	msgs := make([]aiclient.Message, n)
	msgs[0] = msg("system")
	for i := 1; i < n; i++ {
		msgs[i] = msg("user")
	}
	result := trimHistory(msgs)
	if result[0].Role != "system" {
		t.Fatal("system prompt must be preserved")
	}
	if len(result) > maxHistoryMessages+1 {
		t.Fatalf("expected at most %d messages, got %d", maxHistoryMessages+1, len(result))
	}
}

func TestTrimHistory_PreservesToolCallSequence(t *testing.T) {
	msgs := make([]aiclient.Message, 0, maxHistoryMessages+10)
	msgs = append(msgs, msg("system"))
	for i := 0; i < maxHistoryMessages+5; i++ {
		msgs = append(msgs, msg("user"))
	}
	// Insert a tool-call sequence right at the expected cut point.
	// The cut would fall around index 5 (len - maxHistoryMessages).
	// Place a tool-call sequence there to verify it isn't split.
	cutIdx := len(msgs) - maxHistoryMessages
	msgs[cutIdx] = msgWithToolCalls("assistant")
	msgs[cutIdx+1] = aiclient.Message{Role: "tool", ToolCallID: "tc1"}

	result := trimHistory(msgs)
	if result[0].Role != "system" {
		t.Fatal("system prompt must be preserved")
	}
	// Verify no tool message appears without its preceding assistant+toolcalls
	for i, m := range result {
		if m.Role == "tool" && i > 0 && len(result[i-1].ToolCalls) == 0 && result[i-1].Role != "tool" {
			t.Fatalf("orphaned tool message at index %d", i)
		}
	}
}

func TestTrimHistory_AllToolCalls_NoTrim(t *testing.T) {
	// Edge case: every message after system is a tool-call sequence.
	// If safeCut walks to the end, trimHistory should not trim if len < 2*max.
	n := maxHistoryMessages + 3
	msgs := make([]aiclient.Message, n)
	msgs[0] = msg("system")
	for i := 1; i < n; i++ {
		if i%2 == 1 {
			msgs[i] = msgWithToolCalls("assistant")
		} else {
			msgs[i] = aiclient.Message{Role: "tool", ToolCallID: "tc1"}
		}
	}
	result := trimHistory(msgs)
	if result[0].Role != "system" {
		t.Fatal("system prompt must be preserved")
	}
	// Should return original since n < 2*maxHistoryMessages and safeCut walked to end
	if len(result) != n {
		t.Fatalf("expected no trim (all tool-call seqs), got %d (was %d)", len(result), n)
	}
}

func TestTrimHistory_VeryLong_ForceTrim(t *testing.T) {
	// When len > 2*maxHistoryMessages and safeCut exhausts, force trim.
	n := maxHistoryMessages*2 + 5
	msgs := make([]aiclient.Message, n)
	msgs[0] = msg("system")
	for i := 1; i < n; i++ {
		if i%2 == 1 {
			msgs[i] = msgWithToolCalls("assistant")
		} else {
			msgs[i] = aiclient.Message{Role: "tool", ToolCallID: "tc1"}
		}
	}
	result := trimHistory(msgs)
	if result[0].Role != "system" {
		t.Fatal("system prompt must be preserved")
	}
	if len(result) > maxHistoryMessages+1 {
		t.Fatalf("expected forced trim to %d, got %d", maxHistoryMessages+1, len(result))
	}
}

func TestBuildChatToolSetWithOptions_WithoutShell(t *testing.T) {
	tools := buildChatToolSetWithOptions(false)
	for _, tool := range tools {
		if tool.Function.Name == "exec_shell" {
			t.Fatal("exec_shell should NOT be present when allowShell=false")
		}
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools (call_tool, list_tools), got %d", len(tools))
	}
}

func TestBuildChatToolSetWithOptions_WithShell(t *testing.T) {
	tools := buildChatToolSetWithOptions(true)
	found := false
	for _, tool := range tools {
		if tool.Function.Name == "exec_shell" {
			found = true
		}
	}
	if !found {
		t.Fatal("exec_shell should be present when allowShell=true")
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
}

func TestHandleMessage_PreservesReasoningContentAcrossToolCalls(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch requests {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "chatcmpl-1",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "",
						"reasoning_content": "我需要先查看监听端口。",
						"tool_calls": [{
							"id": "call_1",
							"type": "function",
							"function": {
								"name": "call_tool",
								"arguments": "{\"name\":\"network_listen_ports\",\"tool_args\":\"{}\"}"
							}
						}]
					},
					"finish_reason": "tool_calls"
				}],
				"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
			}`))
		case 2:
			messages, _ := raw["messages"].([]any)
			if len(messages) < 4 {
				t.Fatalf("expected at least 4 messages, got %d", len(messages))
			}
			assistant, _ := messages[2].(map[string]any)
			if got, _ := assistant["reasoning_content"].(string); got == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"Missing ` + "`reasoning_content`" + ` field in the assistant message at message index 2"}}`))
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id": "chatcmpl-2",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "当前监听端口有 80 和 443。"
					},
					"finish_reason": "stop"
				}],
				"usage": {"prompt_tokens": 12, "completion_tokens": 6, "total_tokens": 18}
			}`))
		default:
			t.Fatalf("unexpected request #%d", requests)
		}
	}))
	defer srv.Close()

	registry := diagnose.NewToolRegistry()
	registry.RegisterCategory("network", "network", "network tools", diagnose.ToolScopeLocal)
	registry.Register("network", diagnose.DiagnoseTool{
		Name:        "network_listen_ports",
		Description: "list listen ports",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			return "80\n443", nil
		},
	})

	fc := aiclient.NewFailoverClient(config.AIConfig{
		ModelPriority: []string{"deepseek"},
		Models: map[string]config.ModelConfig{
			"deepseek": {
				BaseURL: srv.URL,
				APIKey:  "test-key",
				Model:   "deepseek-reasoner",
			},
		},
		RequestTimeout: config.Duration(5 * time.Second),
	})

	sess := NewChatSession(SessionConfig{
		FC:          fc,
		Registry:    registry,
		ToolTimeout: 2 * time.Second,
		IO:          testChatIO{},
	})

	reply, _, err := sess.HandleMessage(context.Background(), "机器上有哪些端口")
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}
	if reply != "当前监听端口有 80 和 443。" {
		t.Fatalf("reply = %q, want %q", reply, "当前监听端口有 80 和 443。")
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}
