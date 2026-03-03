package diagnose

import "github.com/cprobe/catpaw/diagnose/aiclient"

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
				Name:        "list_tools",
				Description: "查看某个工具类别下所有工具的详细参数说明（仅在需要参数细节时使用）",
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
				Description: "调用一个非直接注入的诊断工具（工具名和参数见系统提示中的工具目录）",
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
