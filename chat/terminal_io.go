package chat

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/pkg/term"
	"github.com/ergochat/readline"
)

// terminalShellExecutor implements diagnose.ShellExecutor for local terminal chat.
type terminalShellExecutor struct {
	rl          *readline.Instance
	autoApprove *bool
}

func (t *terminalShellExecutor) ExecuteShell(ctx context.Context, command string, timeout time.Duration) (string, bool, error) {
	fmt.Printf("\n\033[33m! AI requests command:\033[0m %s\n", command)

	if *t.autoApprove {
		fmt.Printf("\033[33m  (auto-approved)\033[0m\n")
		output, err := ExecShell(ctx, command, timeout)
		return output, true, err
	}

	defer t.rl.SetPrompt(chatPrompt)
	t.rl.SetPrompt("\033[33mConfirm? [y/n/e(edit)/a(all)]:\033[0m ")
	line, err := t.rl.Readline()
	if err != nil {
		if err == readline.ErrInterrupt || err == io.EOF {
			fmt.Println("\n\033[31m操作已取消，程序退出。\033[0m")
			os.Exit(0)
		}
		return "", false, nil
	}
	answer := strings.TrimSpace(strings.ToLower(line))

	switch answer {
	case "y", "yes":
		output, err := ExecShell(ctx, command, timeout)
		return output, true, err
	case "a", "all":
		*t.autoApprove = true
		output, err := ExecShell(ctx, command, timeout)
		return output, true, err
	case "e", "edit":
		t.rl.SetPrompt("\033[33mEnter modified command:\033[0m ")
		edited, err := t.rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt || err == io.EOF {
				fmt.Println("\n\033[31m操作已取消，程序退出。\033[0m")
				os.Exit(0)
			}
			return "", false, nil
		}
		cmd := strings.TrimSpace(edited)
		if cmd == "" {
			return "", false, nil
		}
		fmt.Printf("\033[33mWill execute:\033[0m %s\n", cmd)
		output, err := ExecShell(ctx, cmd, timeout)
		return output, true, err
	default:
		return "", false, nil
	}
}

// newTerminalProgressCallback returns a ProgressCallback for terminal display.
func newTerminalProgressCallback(verbose bool) diagnose.ProgressCallback {
	var spinner *term.Spinner

	return func(event diagnose.ProgressEvent) {
		switch event.Type {
		case diagnose.ProgressAIStart:
			spinner = term.StartSpinner(fmt.Sprintf("[round %d] ⟳ thinking...", event.Round))
		case diagnose.ProgressAIDone:
			if spinner != nil {
				spinner.Stop()
				spinner = nil
			}
			if event.Duration > 0 {
				term.PrintThinkingDone(event.Round, event.Duration)
			}
			if event.Reasoning != "" {
				term.PrintAIReasoning(event.Reasoning)
			}
		case diagnose.ProgressToolStart:
			if event.ToolName == "exec_shell" {
				fmt.Printf("  %s▶ exec_shell%s %s%s%s\n",
					term.ColorYellow, term.ColorReset, term.ColorGray, event.ToolArgs, term.ColorReset)
				return
			}
			term.PrintToolStart(event.ToolName, event.ToolArgs)
		case diagnose.ProgressToolDone:
			if event.ToolName == "exec_shell" {
				return
			}
			term.PrintToolDone(event.ToolName, event.ToolArgs, event.Duration, event.ResultLen, event.IsError)
			if verbose && !event.IsError && event.ToolOutput != "" {
				term.PrintToolOutput(event.ToolOutput, 5)
			}
		}
	}
}
