package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chzyer/readline"
	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/diagnose/aiclient"
	"github.com/cprobe/catpaw/mcp"
	"github.com/cprobe/catpaw/plugins"
)

const (
	maxHistoryMessages = 40
	chatPrompt         = "\033[32m> \033[0m"
)

// Run starts the interactive chat REPL.
func Run(verbose bool) error {
	cfg := config.Config.AI
	if !cfg.Enabled {
		return fmt.Errorf("AI is not enabled, set [ai] enabled = true in config.toml")
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid AI config: %w", err)
	}

	registry := diagnose.NewToolRegistry()
	for _, creator := range plugins.PluginCreators {
		plugins.MayRegisterDiagnoseTools(creator(), registry)
	}
	for _, r := range plugins.DiagnoseRegistrars {
		r(registry)
	}

	// Start MCP servers if configured
	mcpMgr := mcp.NewManager()
	if cfg.MCP.Enabled && len(cfg.MCP.Servers) > 0 {
		mcpCtx, mcpCancel := context.WithCancel(context.Background())
		mcpMgr.StartAll(mcpCtx, cfg.MCP, registry)
		defer func() {
			mcpCancel()
			mcpMgr.Close()
		}()
		if n := mcpMgr.ServerCount(); n > 0 {
			fmt.Printf("MCP: %d server(s) connected\n", n)
		}
	}

	client := aiclient.NewClient(aiclient.ClientConfig{
		BaseURL:        cfg.BaseURL,
		APIKey:         cfg.APIKey,
		Model:          cfg.Model,
		MaxTokens:      cfg.MaxTokens,
		RequestTimeout: time.Duration(cfg.RequestTimeout),
	})

	snapshot := collectSnapshot(registry)
	mcpIdentity := mcpMgr.IdentitySummary(cfg.MCP.DefaultIdentity)
	systemPrompt := buildChatSystemPrompt(registry, snapshot, mcpIdentity, cfg.Language)
	aiTools := buildChatToolSet()
	toolTimeout := time.Duration(cfg.ToolTimeout)
	retryCfg := aiclient.RetryConfig{
		MaxRetries:   cfg.MaxRetries,
		RetryBackoff: time.Duration(cfg.RetryBackoff),
	}

	messages := []aiclient.Message{
		{Role: "system", Content: systemPrompt},
	}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          chatPrompt,
		InterruptPrompt: "^C",
	})
	if err != nil {
		return fmt.Errorf("init readline: %w", err)
	}
	defer rl.Close()

	fmt.Println("catpaw chat - type your question to start, type exit or Ctrl+C to quit")
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigCh:
			fmt.Println("\nbye!")
			cancel()
			rl.Close()
			os.Exit(0)
		case <-done:
			return
		}
	}()
	defer signal.Stop(sigCh)

	for {
		line, readErr := rl.Readline()
		if readErr == readline.ErrInterrupt {
			if line == "" {
				fmt.Println("bye!")
				break
			}
			continue
		}
		if readErr != nil {
			fmt.Println("bye!")
			break
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			fmt.Println("bye!")
			break
		}

		messages = append(messages, aiclient.Message{
			Role:    "user",
			Content: input,
		})

		messages = trimHistory(messages)

		msgCount := len(messages)

		var reply string
		reply, messages, err = runConversationTurn(ctx, client, retryCfg, registry, aiTools, messages, toolTimeout, rl, verbose)
		if err != nil {
			fmt.Printf("\033[31merror: %v\033[0m\n\n", err)
			messages = messages[:msgCount-1]
			continue
		}

		fmt.Println()
		fmt.Println(reply)
		fmt.Println()
	}
	return nil
}

func buildChatToolSet() []aiclient.Tool {
	return []aiclient.Tool{
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
		{
			Type: "function",
			Function: aiclient.ToolFunction{
				Name:        "exec_shell",
				Description: "Execute a shell command (requires user confirmation). Use when built-in tools are insufficient.",
				Parameters: &aiclient.Parameters{
					Type: "object",
					Properties: map[string]aiclient.Property{
						"command": {Type: "string", Description: "Shell command to execute"},
					},
					Required: []string{"command"},
				},
			},
		},
	}
}

// runConversationTurn handles one user message and all subsequent tool-calling
// rounds until the AI produces a final text response.
func runConversationTurn(
	ctx context.Context,
	client *aiclient.Client,
	retryCfg aiclient.RetryConfig,
	registry *diagnose.ToolRegistry,
	aiTools []aiclient.Tool,
	messages []aiclient.Message,
	toolTimeout time.Duration,
	rl *readline.Instance,
	verbose bool,
) (string, []aiclient.Message, error) {
	const maxRounds = 20

	for round := 0; round < maxRounds; round++ {
		roundNum := round + 1

		sp := startSpinner(fmt.Sprintf("[round %d] ⟳ thinking...", roundNum))
		start := time.Now()
		resp, err := aiclient.ChatWithRetry(ctx, client, retryCfg, messages, aiTools)
		sp.stop()
		printThinkingDone(roundNum, time.Since(start))
		if err != nil {
			return "", messages, fmt.Errorf("AI API call failed: %w", err)
		}

		if len(resp.Choices) == 0 {
			return "", messages, fmt.Errorf("AI returned empty response")
		}

		choice := resp.Choices[0]
		content := choice.Message.Content
		toolCalls := choice.Message.ToolCalls

		if len(toolCalls) == 0 {
			messages = append(messages, aiclient.Message{
				Role:    "assistant",
				Content: content,
			})
			return content, messages, nil
		}

		if content != "" {
			printAIReasoning(content)
		}

		messages = append(messages, aiclient.Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		})

		for _, tc := range toolCalls {
			name := tc.Function.Name
			argsDisplay := formatToolArgsDisplay(name, tc.Function.Arguments)

			if name == "exec_shell" {
				fmt.Printf("  %s▶ exec_shell%s %s%s%s\n", colorYellow, colorReset, colorGray, argsDisplay, colorReset)
				result := executeChatTool(ctx, registry, name, tc.Function.Arguments, toolTimeout, rl)
				isErr := strings.HasPrefix(result, "error:")
				if verbose && !isErr {
					printToolOutput(result, 5)
				}
				messages = append(messages, aiclient.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    result,
				})
			} else {
				printToolStart(name, argsDisplay)
				toolStart := time.Now()
				result := executeChatTool(ctx, registry, name, tc.Function.Arguments, toolTimeout, rl)
				isErr := strings.HasPrefix(result, "error:")
				printToolDone(name, argsDisplay, time.Since(toolStart), len(result), isErr)
				if verbose && !isErr {
					printToolOutput(result, 5)
				}
				messages = append(messages, aiclient.Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    result,
				})
			}
		}
	}

	return "[incomplete] max tool-calling rounds reached", messages, nil
}

func executeChatTool(ctx context.Context, registry *diagnose.ToolRegistry, name, rawArgs string, toolTimeout time.Duration, rl *readline.Instance) string {
	args := parseArgs(rawArgs)

	switch name {
	case "list_tools":
		category := args["category"]
		if category == "" {
			return "error: list_tools requires 'category' parameter"
		}
		return registry.ListTools(category)

	case "call_tool":
		toolName := args["name"]
		if toolName == "" {
			return "error: call_tool requires 'name' parameter"
		}
		tool, ok := registry.Get(toolName)
		if !ok {
			return "error: unknown tool: " + toolName
		}
		toolArgs := parseToolArgs(args["tool_args"])
		toolCtx, cancel := context.WithTimeout(ctx, toolTimeout)
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
		result, err := execShellInteractive(ctx, rl, command, toolTimeout)
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

func parseArgs(raw string) map[string]string {
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

// trimHistory keeps the system prompt and the most recent messages to stay
// within context window limits. It never splits a tool-call sequence: an
// assistant message with ToolCalls and its subsequent tool-response messages
// are kept together as a unit.
//
// As a safety net, if no safe cut point is found (e.g. the tail is one huge
// tool-call sequence), a hard cap of 2× maxHistoryMessages forces a trim to
// prevent unbounded memory growth.
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
