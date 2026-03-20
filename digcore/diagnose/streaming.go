package diagnose

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose/aiclient"
)

// ShellExecutor is an interface for executing shell commands during chat.
// Implementations must handle user approval and command editing.
type ShellExecutor interface {
	// ExecuteShell executes a shell command with approval flow.
	// Returns (output, approved, error).
	// approved=false indicates user rejected execution.
	ExecuteShell(ctx context.Context, command string, timeout time.Duration) (string, bool, error)
}

// ChatStreamConfig configures a new ChatStream.
type ChatStreamConfig struct {
	FC                 *aiclient.FailoverClient
	Registry           *ToolRegistry
	ToolTimeout        time.Duration
	SystemPrompt       string
	AllowShell         bool
	ShellExecutor      ShellExecutor
	ProgressCallback   ProgressCallback
	ContextWindowLimit int
	GatewayMetadata    aiclient.GatewayMetadata
}

// ChatStream manages a multi-turn chat conversation with history.
// Unlike DiagnoseEngine which processes single diagnosis requests,
// ChatStream maintains conversation state across multiple user messages.
type ChatStream struct {
	fc                 *aiclient.FailoverClient
	registry           *ToolRegistry
	aiTools            []aiclient.Tool
	messages           []aiclient.Message
	toolTimeout        time.Duration
	shellExecutor      ShellExecutor
	progressCallback   ProgressCallback
	contextWindowLimit int
	gatewayMetadata    aiclient.GatewayMetadata
}

const (
	chatMaxRounds      = 20
	maxHistoryMessages = 40
)

// NewChatStream creates a chat stream with the given configuration.
func NewChatStream(cfg ChatStreamConfig) *ChatStream {
	aiTools := buildChatToolSet(cfg.AllowShell)

	return &ChatStream{
		fc:                 cfg.FC,
		registry:           cfg.Registry,
		aiTools:            aiTools,
		messages:           []aiclient.Message{{Role: "system", Content: cfg.SystemPrompt}},
		toolTimeout:        cfg.ToolTimeout,
		shellExecutor:      cfg.ShellExecutor,
		progressCallback:   cfg.ProgressCallback,
		contextWindowLimit: cfg.ContextWindowLimit,
		gatewayMetadata:    cfg.GatewayMetadata,
	}
}

// HandleMessage processes one user message through the conversation.
// On error, the user message is rolled back from history.
func (s *ChatStream) HandleMessage(ctx context.Context, input string) (reply string, usage aiclient.Usage, err error) {
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
func (s *ChatStream) conversationLoop(ctx context.Context) (string, []aiclient.Message, aiclient.Usage, error) {
	var totalUsage aiclient.Usage

	for round := 0; round < chatMaxRounds; round++ {
		if ctx.Err() != nil {
			return "", s.messages, totalUsage, ctx.Err()
		}

		if s.contextWindowLimit > 0 {
			s.messages = aiclient.CompactMessages(s.messages, s.contextWindowLimit)
		}

		roundNum := round + 1
		emitProgress(s.progressCallback, ProgressEvent{Type: ProgressAIStart, Round: roundNum})

		start := time.Now()
		resp, _, err := s.fc.Chat(ctx, s.messages, s.aiTools)
		elapsed := time.Since(start)
		emitProgress(s.progressCallback, ProgressEvent{Type: ProgressAIDone, Round: roundNum, Duration: elapsed})

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
			emitProgress(s.progressCallback, ProgressEvent{
				Type: ProgressAIDone,
				Round: roundNum,
				Reasoning: content,
			})
		}

		s.messages = append(s.messages, aiclient.Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		})

		for _, tc := range toolCalls {
			name := tc.Function.Name
			argsDisplay := FormatToolArgsDisplay(name, tc.Function.Arguments)

			emitProgress(s.progressCallback, ProgressEvent{
				Type:     ProgressToolStart,
				Round:    roundNum,
				ToolName: name,
				ToolArgs: argsDisplay,
			})

			toolStart := time.Now()
			result, err := s.executeTool(ctx, name, tc.Function.Arguments)
			toolElapsed := time.Since(toolStart)

			isErr := err != nil
			if err != nil {
				result = "error: " + err.Error()
			}
			result = TruncateOutput(result)

			emitProgress(s.progressCallback, ProgressEvent{
				Type:       ProgressToolDone,
				Round:      roundNum,
				ToolName:   name,
				ToolArgs:   argsDisplay,
				Duration:   toolElapsed,
				ResultLen:  len(result),
				IsError:    isErr,
				ToolOutput: result,
			})

			s.messages = append(s.messages, aiclient.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}

	return "[incomplete] max tool-calling rounds reached", s.messages, totalUsage, nil
}

// executeTool routes a tool call to appropriate handler.
func (s *ChatStream) executeTool(ctx context.Context, name, rawArgs string) (string, error) {
	args := ParseArgs(rawArgs)

	switch name {
	case "list_tool_categories":
		return s.registry.ListCategoriesForOS(runtime.GOOS), nil

	case "list_tools":
		category := args["category"]
		if category == "" {
			return "", fmt.Errorf("list_tools requires 'category' parameter")
		}
		return s.registry.ListToolsForOS(category, runtime.GOOS), nil

	case "call_tool":
		toolName := args["name"]
		if toolName == "" {
			return "", fmt.Errorf("call_tool requires 'name' parameter")
		}
		tool, ok := s.registry.Get(toolName)
		if !ok {
			return "", fmt.Errorf("unknown tool: %s", toolName)
		}
		if tool.Scope == ToolScopeRemote {
			return "", fmt.Errorf("tool %s requires a remote connection (not available in chat mode)", tool.Name)
		}
		if tool.Execute == nil {
			return "", fmt.Errorf("tool %s has no Execute function", tool.Name)
		}
		toolArgs := ParseToolArgs(args["tool_args"])
		toolCtx, cancel := context.WithTimeout(ctx, s.toolTimeout)
		defer cancel()
		return tool.Execute(toolCtx, toolArgs)

	case "exec_shell":
		if s.shellExecutor == nil {
			return "", fmt.Errorf("shell execution not enabled (no ShellExecutor configured)")
		}
		command := args["command"]
		if command == "" {
			return "", fmt.Errorf("exec_shell requires 'command' parameter")
		}
		toolCtx, cancel := context.WithTimeout(ctx, s.toolTimeout)
		defer cancel()
		output, approved, err := s.shellExecutor.ExecuteShell(toolCtx, command, s.toolTimeout)
		if !approved {
			return "user rejected command execution", nil
		}
		return output, err

	default:
		// Try as direct tool call
		tool, ok := s.registry.Get(name)
		if !ok {
			return "", fmt.Errorf("unknown tool: %s", name)
		}
		if tool.Execute == nil {
			return "", fmt.Errorf("tool %s has no Execute function", tool.Name)
		}
		toolCtx, cancel := context.WithTimeout(ctx, s.toolTimeout)
		defer cancel()
		return tool.Execute(toolCtx, args)
	}
}

// buildChatToolSet constructs the AI tool definitions for chat mode.
func buildChatToolSet(allowShell bool) []aiclient.Tool {
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
				Name:        "list_tool_categories",
				Description: "List all available tool categories (plugins). Use this to explore available diagnostic areas.",
				Parameters: &aiclient.Parameters{
					Type:       "object",
					Properties: map[string]aiclient.Property{},
					Required:   []string{},
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

// trimHistory keeps the system prompt and most recent messages to stay
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

// Messages returns the current message history. Useful for inspection or persistence.
func (s *ChatStream) Messages() []aiclient.Message {
	return s.messages
}

// Reset clears the conversation history except for the system prompt.
func (s *ChatStream) Reset() {
	if len(s.messages) > 0 && s.messages[0].Role == "system" {
		s.messages = []aiclient.Message{s.messages[0]}
	} else {
		s.messages = []aiclient.Message{}
	}
}
