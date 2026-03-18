package chat

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cprobe/digcore/config"
	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/diagnose/aiclient"
	"github.com/cprobe/digcore/mcp"
	"github.com/cprobe/digcore/plugins"
	"github.com/ergochat/readline"
)

const chatPrompt = "\033[32m> \033[0m"

// Run starts the interactive chat REPL.
// If modelPin is non-empty, only that model is used (no failover).
func Run(verbose bool, modelPin string) error {
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

	fc := aiclient.NewFailoverClientForScene(cfg, "chat")
	if modelPin != "" {
		if fc.IsGateway() {
			return fmt.Errorf("--model is not supported when ai.gateway.enabled=true")
		}
		if err := fc.PinModel(modelPin); err != nil {
			return fmt.Errorf("--model flag: %w", err)
		}
	}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          chatPrompt,
		InterruptPrompt: "^C",
	})
	if err != nil {
		return fmt.Errorf("init readline: %w", err)
	}
	defer rl.Close()

	autoApprove := false
	io := &terminalChatIO{
		rl:          rl,
		verbose:     verbose,
		autoApprove: &autoApprove,
	}

	snapshot := CollectSnapshot(registry)
	mcpIdentity := mcpMgr.IdentitySummary(cfg.MCP.DefaultIdentity)

	sess := NewChatSession(SessionConfig{
		FC:                 fc,
		Registry:           registry,
		ToolTimeout:        time.Duration(cfg.ToolTimeout),
		IO:                 io,
		AllowShell:         true,
		Language:           cfg.Language,
		Snapshot:           snapshot,
		MCPIdentity:        mcpIdentity,
		ContextWindowLimit: cfg.ContextWindowLimit(),
		GatewayMetadata:    aiclient.GatewayMetadata{RequestSource: "local_chat"},
	})

	printChatBanner(fc)

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

		if handleSlashCommand(input, fc, &cfg) {
			continue
		}

		reply, usage, err := sess.HandleMessage(ctx, input)
		if err != nil {
			fmt.Printf("\033[31merror: %v\033[0m\n\n", err)
			continue
		}

		fmt.Println()
		fmt.Println(reply)
		if len(cfg.ModelPriority) > 0 && len(cfg.Models) > 0 {
			primary := cfg.PrimaryModel()
			printTokenUsage(usage, primary.InputPrice, primary.OutputPrice)
		} else {
			printTokenUsage(usage, 0, 0)
		}
		fmt.Println()
	}
	return nil
}

func printChatBanner(fc *aiclient.FailoverClient) {
	fmt.Print("catpaw chat")
	if fc.IsGateway() {
		fmt.Printf(" [gateway: %s]", strings.Join(fc.ModelNames(), ", "))
		fmt.Println(" - type your question, /models for gateway info, exit or Ctrl+C to quit")
		fmt.Println()
		return
	}
	if pinned := fc.PinnedModel(); pinned != "" {
		fmt.Printf(" [model: %s]", pinned)
	} else {
		names := fc.ModelNames()
		if len(names) == 1 {
			fmt.Printf(" [model: %s]", names[0])
		} else {
			fmt.Printf(" [models: %s]", strings.Join(names, " → "))
		}
	}
	fmt.Println(" - type your question, /models for model info, exit or Ctrl+C to quit")
	fmt.Println()
}

// handleSlashCommand processes /model and /models commands.
// Returns true if the input was a slash command (handled), false otherwise.
func handleSlashCommand(input string, fc *aiclient.FailoverClient, cfg *config.AIConfig) bool {
	if input == "/models" {
		printModelList(fc, cfg)
		return true
	}
	if fc.IsGateway() && strings.HasPrefix(input, "/model") {
		fmt.Println("\033[33m/model is disabled when ai.gateway.enabled=true; routing is controlled by server\033[0m")
		fmt.Println()
		return true
	}

	if input == "/model auto" {
		fc.PinModel("")
		names := fc.ModelNames()
		fmt.Printf("\033[33mSwitched to auto mode (priority: %s)\033[0m\n\n", strings.Join(names, " → "))
		return true
	}

	if strings.HasPrefix(input, "/model ") {
		name := strings.TrimSpace(strings.TrimPrefix(input, "/model "))
		if name == "" {
			printModelList(fc, cfg)
			return true
		}
		if err := fc.PinModel(name); err != nil {
			fmt.Printf("\033[31m%v\033[0m\n", err)
			fmt.Printf("Available: %s\n\n", strings.Join(fc.ModelNames(), ", "))
		} else {
			fmt.Printf("\033[33mSwitched to %s\033[0m\n\n", name)
		}
		return true
	}

	return false
}

func printModelList(fc *aiclient.FailoverClient, cfg *config.AIConfig) {
	if fc.IsGateway() {
		fmt.Println("  gateway mode: requests are sent to server")
		if len(fc.ModelNames()) > 0 {
			fmt.Printf("  route = %s\n", strings.Join(fc.ModelNames(), " -> "))
		}
		fmt.Println("  (/model is disabled; server selects the upstream model)")
		fmt.Println()
		return
	}
	pinned := fc.PinnedModel()
	for i, name := range fc.ModelNames() {
		m := cfg.Models[name]
		marker := "  "
		if pinned != "" && name == pinned {
			marker = "* "
		} else if pinned == "" && i == 0 {
			marker = "* "
		}
		fmt.Printf("  %s\033[33m%s\033[0m  model=%s  base_url=%s\n", marker, name, m.Model, m.BaseURL)
	}
	if pinned != "" {
		fmt.Printf("  (pinned to %s, use /model auto to restore failover)\n", pinned)
	} else {
		fmt.Println("  (auto mode: tries models in priority order)")
	}
	fmt.Println()
}
