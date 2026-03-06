package diagnose

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"
)

const (
	maxToolOutputBytes  = 32 * 1024 // 32KB per tool output sent to AI
	maxRecordResultBytes = 64 * 1024 // 64KB per tool result stored in record
)

// executeTool routes a tool call to the appropriate handler:
// meta-tools (list_tool_categories, list_tools, call_tool) or direct-inject tools.
func executeTool(ctx context.Context, registry *ToolRegistry, session *DiagnoseSession, name string, rawArgs string) (string, error) {
	args := ParseArgs(rawArgs)

	switch name {
	case "list_tool_categories":
		return registry.ListCategories(), nil

	case "list_tools":
		category := args["category"]
		if category == "" {
			return "", fmt.Errorf("list_tools requires 'category' parameter")
		}
		return registry.ListTools(category), nil

	case "call_tool":
		toolName := args["name"]
		if toolName == "" {
			return "", fmt.Errorf("call_tool requires 'name' parameter")
		}
		tool, ok := registry.Get(toolName)
		if !ok {
			return "", fmt.Errorf("unknown tool: %s", toolName)
		}
		toolArgs := ParseToolArgs(args["tool_args"])
		return executeToolImpl(ctx, session, *tool, toolArgs)

	default:
		tool, ok := registry.Get(name)
		if !ok {
			return "", fmt.Errorf("unknown tool: %s", name)
		}
		return executeToolImpl(ctx, session, *tool, args)
	}
}

func executeToolImpl(ctx context.Context, session *DiagnoseSession, tool DiagnoseTool, args map[string]string) (string, error) {
	if tool.Scope == ToolScopeLocal {
		if tool.Execute == nil {
			return "", fmt.Errorf("tool %s has no Execute function", tool.Name)
		}
		return tool.Execute(ctx, args)
	}

	if tool.RemoteExecute == nil {
		return "", fmt.Errorf("tool %s has no RemoteExecute function", tool.Name)
	}
	if session == nil {
		return "", fmt.Errorf("no session available for remote tool %s", tool.Name)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return tool.RemoteExecute(ctx, session, args)
}

// ParseArgs parses the AI's function call arguments JSON into a flat string map.
// Returns a non-nil empty map for empty input so callers can safely read keys.
func ParseArgs(raw string) map[string]string {
	if raw == "" {
		return make(map[string]string)
	}
	m := unmarshalStringMap(raw)
	if m == nil {
		return map[string]string{"_raw": raw}
	}
	return m
}

// ParseToolArgs parses the nested tool_args JSON string (from call_tool).
// Returns nil for empty input; callers should check for nil.
func ParseToolArgs(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	m := unmarshalStringMap(raw)
	if m == nil {
		return map[string]string{"_raw": raw}
	}
	return m
}

// unmarshalStringMap attempts to parse a JSON object into map[string]string.
// First tries direct string-valued map; on failure, falls back to map[string]any
// and coerces each value via anyToString. Returns nil if the input is not a
// valid JSON object at all.
func unmarshalStringMap(raw string) map[string]string {
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		return m
	}
	var anyMap map[string]any
	if err := json.Unmarshal([]byte(raw), &anyMap); err != nil {
		return nil
	}
	m = make(map[string]string, len(anyMap))
	for k, v := range anyMap {
		m[k] = anyToString(v)
	}
	return m
}

// anyToString converts an arbitrary JSON-decoded value to a string.
//   - nil        → "" (so "required" checks fire cleanly)
//   - float64    → decimal notation via strconv (avoids scientific notation)
//   - object/arr → re-serialized as JSON for downstream parsing
//   - others     → fmt.Sprint
func anyToString(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case map[string]any, []any:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	default:
		return fmt.Sprint(val)
	}
}

// FormatToolArgsDisplay produces a human-friendly summary of tool arguments
// for terminal display. Returns "" when there is nothing meaningful to show.
func FormatToolArgsDisplay(name, rawArgs string) string {
	args := ParseArgs(rawArgs)
	switch name {
	case "call_tool":
		toolName := args["name"]
		toolArgs := args["tool_args"]
		if toolArgs != "" && toolArgs != "{}" {
			return toolName + " " + toolArgs
		}
		return toolName
	case "list_tools":
		return args["category"]
	case "exec_shell":
		cmd := args["command"]
		if utf8.RuneCountInString(cmd) > 80 {
			runes := []rune(cmd)
			cmd = string(runes[:77]) + "..."
		}
		return cmd
	default:
		return ""
	}
}

// TruncateOutput ensures a tool's output doesn't exceed the maximum size
// for sending to the AI. Uses UTF-8-safe truncation.
func TruncateOutput(s string) string {
	if len(s) <= maxToolOutputBytes {
		return s
	}
	return TruncateUTF8(s, maxToolOutputBytes) + "\n...[output truncated]"
}

// TruncateForRecord truncates tool output for storage in DiagnoseRecord.
// Allows a larger budget than TruncateOutput since records are for audit.
func TruncateForRecord(s string) string {
	if len(s) <= maxRecordResultBytes {
		return s
	}
	return TruncateUTF8(s, maxRecordResultBytes) + "\n...[record truncated]"
}
