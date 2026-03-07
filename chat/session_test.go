package chat

import (
	"testing"

	"github.com/cprobe/catpaw/diagnose/aiclient"
)

func msg(role string) aiclient.Message { return aiclient.Message{Role: role} }

func msgWithToolCalls(role string) aiclient.Message {
	return aiclient.Message{
		Role:      role,
		ToolCalls: []aiclient.ToolCall{{ID: "tc1", Function: aiclient.FunctionCall{Name: "f"}}},
	}
}

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
