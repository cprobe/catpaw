package diagnose

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cprobe/catpaw/diagnose/aiclient"
)

const maxToolOutputBytes = 32 * 1024 // 32KB per tool output

// buildToolSet constructs the tool definitions sent to the AI.
// Direct-inject tools come from the triggering plugin; meta-tools enable
// progressive discovery of all other tools.
func buildToolSet(registry *ToolRegistry, req *DiagnoseRequest) ([]aiclient.Tool, []DiagnoseTool) {
	var aiTools []aiclient.Tool
	directTools := registry.ByPlugin(req.Plugin)

	for _, t := range directTools {
		aiTools = append(aiTools, diagnoseToolToAI(t))
	}

	aiTools = append(aiTools, metaTools()...)
	return aiTools, directTools
}

func metaTools() []aiclient.Tool {
	return []aiclient.Tool{
		{
			Type: "function",
			Function: aiclient.ToolFunction{
				Name:        "list_tool_categories",
				Description: "列出所有可用的诊断工具大类（如 disk、cpu、memory、redis 等）",
			},
		},
		{
			Type: "function",
			Function: aiclient.ToolFunction{
				Name:        "list_tools",
				Description: "列出某个大类下的所有诊断工具及其参数说明",
				Parameters: &aiclient.Parameters{
					Type: "object",
					Properties: map[string]aiclient.Property{
						"category": {Type: "string", Description: "工具大类名称"},
					},
					Required: []string{"category"},
				},
			},
		},
		{
			Type: "function",
			Function: aiclient.ToolFunction{
				Name:        "call_tool",
				Description: "调用一个非直接注入的诊断工具",
				Parameters: &aiclient.Parameters{
					Type: "object",
					Properties: map[string]aiclient.Property{
						"name":      {Type: "string", Description: "工具名称"},
						"tool_args": {Type: "string", Description: "工具参数，JSON 字符串格式"},
					},
					Required: []string{"name"},
				},
			},
		},
	}
}

// diagnoseToolToAI converts a DiagnoseTool to the AI function-calling format.
func diagnoseToolToAI(t DiagnoseTool) aiclient.Tool {
	tool := aiclient.Tool{
		Type: "function",
		Function: aiclient.ToolFunction{
			Name:        t.Name,
			Description: t.Description,
		},
	}
	if len(t.Parameters) > 0 {
		props := make(map[string]aiclient.Property, len(t.Parameters))
		var required []string
		for _, p := range t.Parameters {
			props[p.Name] = aiclient.Property{
				Type:        p.Type,
				Description: p.Description,
			}
			if p.Required {
				required = append(required, p.Name)
			}
		}
		tool.Function.Parameters = &aiclient.Parameters{
			Type:       "object",
			Properties: props,
			Required:   required,
		}
	}
	return tool
}

// executeTool routes a tool call to the appropriate handler:
// meta-tools (list_tool_categories, list_tools, call_tool) or direct-inject tools.
func executeTool(ctx context.Context, registry *ToolRegistry, req *DiagnoseRequest, name string, rawArgs string) (string, error) {
	args := parseArgs(rawArgs)

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
		toolArgs := parseToolArgs(args["tool_args"])
		return executeToolImpl(ctx, req, *tool, toolArgs)

	default:
		tool, ok := registry.Get(name)
		if !ok {
			return "", fmt.Errorf("unknown tool: %s", name)
		}
		return executeToolImpl(ctx, req, *tool, args)
	}
}

func executeToolImpl(ctx context.Context, req *DiagnoseRequest, tool DiagnoseTool, args map[string]string) (string, error) {
	if tool.Scope == ToolScopeLocal {
		if tool.Execute == nil {
			return "", fmt.Errorf("tool %s has no Execute function", tool.Name)
		}
		return tool.Execute(ctx, args)
	}

	if tool.RemoteExecute == nil {
		return "", fmt.Errorf("tool %s has no RemoteExecute function", tool.Name)
	}
	session := req.Session
	if session == nil {
		return "", fmt.Errorf("no session available for remote tool %s", tool.Name)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return tool.RemoteExecute(ctx, session, args)
}

// parseArgs parses the AI's function call arguments JSON into a flat string map.
func parseArgs(raw string) map[string]string {
	if raw == "" {
		return make(map[string]string)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		// Try parsing as map[string]any for numeric/boolean values
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

// parseToolArgs parses the nested tool_args JSON string (from call_tool).
func parseToolArgs(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]string{"_raw": raw}
	}
	return m
}

// TruncateOutput ensures a tool's output doesn't exceed the maximum size.
func TruncateOutput(s string) string {
	if len(s) <= maxToolOutputBytes {
		return s
	}
	return s[:maxToolOutputBytes] + "\n...[output truncated]"
}
