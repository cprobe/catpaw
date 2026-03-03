package diagnose

import (
	"context"
	"encoding/json"
	"fmt"
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
// Exported for reuse by chat/ and other callers.
func ParseArgs(raw string) map[string]string {
	if raw == "" {
		return make(map[string]string)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		var anyMap map[string]any
		if err2 := json.Unmarshal([]byte(raw), &anyMap); err2 != nil {
			return map[string]string{"_raw": raw}
		}
		m = make(map[string]string, len(anyMap))
		for k, v := range anyMap {
			m[k] = fmt.Sprint(v)
		}
	}
	return m
}

// ParseToolArgs parses the nested tool_args JSON string (from call_tool).
// Exported for reuse by chat/ and other callers.
func ParseToolArgs(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]string{"_raw": raw}
	}
	return m
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
