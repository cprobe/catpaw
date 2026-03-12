package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/diagnose/aiclient"
)

const (
	chatMaxRounds      = 20
	maxHistoryMessages = 40
)

// SessionConfig configures a new ChatSession.
type SessionConfig struct {
	FC                 *aiclient.FailoverClient
	Registry           *diagnose.ToolRegistry
	ToolTimeout        time.Duration
	IO                 ChatIO
	AllowShell         bool
	Language           string
	Snapshot           string
	MCPIdentity        string
	ContextWindowLimit int
	GatewayMetadata    aiclient.GatewayMetadata
}

// ChatSession manages a multi-turn chat conversation with history.
type ChatSession struct {
	fc                 *aiclient.FailoverClient
	registry           *diagnose.ToolRegistry
	aiTools            []aiclient.Tool
	messages           []aiclient.Message
	toolTimeout        time.Duration
	io                 ChatIO
	contextWindowLimit int
	gatewayMetadata    aiclient.GatewayMetadata
}

// NewChatSession creates a chat session with the given configuration.
func NewChatSession(cfg SessionConfig) *ChatSession {
	systemPrompt := buildChatSystemPrompt(cfg.Registry, cfg.Snapshot, cfg.MCPIdentity, cfg.Language, cfg.AllowShell)
	aiTools := buildChatToolSetWithOptions(cfg.AllowShell)

	return &ChatSession{
		fc:                 cfg.FC,
		registry:           cfg.Registry,
		aiTools:            aiTools,
		messages:           []aiclient.Message{{Role: "system", Content: systemPrompt}},
		toolTimeout:        cfg.ToolTimeout,
		io:                 cfg.IO,
		contextWindowLimit: cfg.ContextWindowLimit,
		gatewayMetadata:    cfg.GatewayMetadata,
	}
}

// HandleMessage processes one user message through the conversation.
// On error, the user message is rolled back from history.
func (s *ChatSession) HandleMessage(ctx context.Context, input string) (reply string, usage aiclient.Usage, err error) {
	ctx = aiclient.WithGatewayMetadata(ctx, s.gatewayMetadata)
	s.messages = append(s.messages, aiclient.Message{
		Role:    "user",
		Content: input,
	})
	s.messages = trimHistory(s.messages)

	msgCount := len(s.messages)
	reply, s.messages, usage, err = s.conversationLoop(ctx)
	if err != nil {
		s.messages = s.messages[:msgCount-1]
		return "", usage, err
	}
	return reply, usage, nil
}

// conversationLoop runs AI rounds until a final text response or max rounds.
func (s *ChatSession) conversationLoop(ctx context.Context) (string, []aiclient.Message, aiclient.Usage, error) {
	var totalUsage aiclient.Usage

	for round := 0; round < chatMaxRounds; round++ {
		if ctx.Err() != nil {
			return "", s.messages, totalUsage, ctx.Err()
		}

		if s.contextWindowLimit > 0 {
			s.messages = aiclient.CompactMessages(s.messages, s.contextWindowLimit)
		}

		roundNum := round + 1
		s.io.OnThinkingStart(roundNum)
		start := time.Now()
		resp, _, err := s.fc.Chat(ctx, s.messages, s.aiTools)
		s.io.OnThinkingDone(roundNum, time.Since(start))
		if err != nil {
			return "", s.messages, totalUsage, fmt.Errorf("AI API call failed: %w", err)
		}

		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens

		if len(resp.Choices) == 0 {
			return "", s.messages, totalUsage, fmt.Errorf("AI returned empty response")
		}

		choice := resp.Choices[0]
		content := choice.Message.Content
		toolCalls := choice.Message.ToolCalls

		if len(toolCalls) == 0 {
			s.messages = append(s.messages, aiclient.Message{
				Role:    "assistant",
				Content: content,
			})
			return content, s.messages, totalUsage, nil
		}

		if content != "" {
			s.io.OnReasoning(content)
		}

		s.messages = append(s.messages, aiclient.Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		})

		for _, tc := range toolCalls {
			name := tc.Function.Name
			argsDisplay := formatToolArgsDisplay(name, tc.Function.Arguments)

			s.io.OnToolStart(name, argsDisplay)
			toolStart := time.Now()
			result := s.executeTool(ctx, name, tc.Function.Arguments)
			isErr := strings.HasPrefix(result, "error:")
			s.io.OnToolDone(name, argsDisplay, time.Since(toolStart), len(result), isErr)
			if !isErr {
				s.io.OnToolOutput(result)
			}

			s.messages = append(s.messages, aiclient.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}

	return "[incomplete] max tool-calling rounds reached", s.messages, totalUsage, nil
}

func (s *ChatSession) executeTool(ctx context.Context, name, rawArgs string) string {
	args := diagnose.ParseArgs(rawArgs)

	switch name {
	case "list_tools":
		category := args["category"]
		if category == "" {
			return "error: list_tools requires 'category' parameter"
		}
		return s.registry.ListTools(category)

	case "call_tool":
		toolName := args["name"]
		if toolName == "" {
			return "error: call_tool requires 'name' parameter"
		}
		tool, ok := s.registry.Get(toolName)
		if !ok {
			return "error: unknown tool: " + toolName
		}
		toolArgs := diagnose.ParseToolArgs(args["tool_args"])
		toolCtx, cancel := context.WithTimeout(ctx, s.toolTimeout)
		defer cancel()
		result, err := executeLocalTool(toolCtx, *tool, toolArgs)
		if err != nil {
			return "error: " + err.Error()
		}
		return diagnose.TruncateOutput(result)

	case "exec_shell":
		command := args["command"]
		if command == "" {
			return "error: exec_shell requires 'command' parameter"
		}
		approved, editedCmd := s.io.ApproveShell(command)
		if !approved {
			return "user rejected command execution"
		}
		if editedCmd != "" {
			command = editedCmd
		}
		result, err := execShell(ctx, command, s.toolTimeout)
		if err != nil {
			return "error: " + err.Error()
		}
		return result

	default:
		return "error: unknown tool: " + name
	}
}

func executeLocalTool(ctx context.Context, tool diagnose.DiagnoseTool, args map[string]string) (string, error) {
	if tool.Scope == diagnose.ToolScopeRemote {
		return "", fmt.Errorf("tool %s requires a remote connection (not available in chat mode)", tool.Name)
	}
	if tool.Execute == nil {
		return "", fmt.Errorf("tool %s has no Execute function", tool.Name)
	}
	return tool.Execute(ctx, args)
}

func buildChatToolSetWithOptions(allowShell bool) []aiclient.Tool {
	tools := []aiclient.Tool{
		{
			Type: "function",
			Function: aiclient.ToolFunction{
				Name:        "call_tool",
				Description: "Invoke a diagnostic tool by name. All available tools are listed in the system prompt.",
				Parameters: &aiclient.Parameters{
					Type: "object",
					Properties: map[string]aiclient.Property{
						"name":      {Type: "string", Description: "Tool name"},
						"tool_args": {Type: "string", Description: "Tool arguments as JSON string"},
					},
					Required: []string{"name"},
				},
			},
		},
		{
			Type: "function",
			Function: aiclient.ToolFunction{
				Name:        "list_tools",
				Description: "Show detailed parameter info for tools in a category. Use only when you need parameter details not shown in the catalog.",
				Parameters: &aiclient.Parameters{
					Type: "object",
					Properties: map[string]aiclient.Property{
						"category": {Type: "string", Description: "Tool category name"},
					},
					Required: []string{"category"},
				},
			},
		},
	}
	if allowShell {
		tools = append(tools, aiclient.Tool{
			Type: "function",
			Function: aiclient.ToolFunction{
				Name:        "exec_shell",
				Description: "Execute a shell command. Use when built-in tools are insufficient.",
				Parameters: &aiclient.Parameters{
					Type: "object",
					Properties: map[string]aiclient.Property{
						"command": {Type: "string", Description: "Shell command to execute"},
					},
					Required: []string{"command"},
				},
			},
		})
	}
	return tools
}

// trimHistory keeps the system prompt and the most recent messages to stay
// within context window limits. It never splits a tool-call sequence: an
// assistant message with ToolCalls and its subsequent tool-response messages
// are kept together as a unit.
func trimHistory(messages []aiclient.Message) []aiclient.Message {
	if len(messages) <= maxHistoryMessages+1 {
		return messages
	}

	cut := len(messages) - maxHistoryMessages
	safeCut := cut
	for safeCut < len(messages) {
		msg := messages[safeCut]
		if msg.Role == "tool" {
			safeCut++
			continue
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			safeCut++
			continue
		}
		break
	}

	if safeCut < len(messages) {
		cut = safeCut
	} else if len(messages) > maxHistoryMessages*2 {
		cut = len(messages) - maxHistoryMessages
	} else {
		return messages
	}

	result := make([]aiclient.Message, 0, len(messages)-cut+1)
	result = append(result, messages[0])
	result = append(result, messages[cut:]...)
	return result
}
