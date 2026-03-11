package chat

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	
	"github.com/cprobe/catpaw/pkg/term"
	"github.com/ergochat/readline"
)

// terminalChatIO provides terminal-based I/O for local chat REPL.
type terminalChatIO struct {
	rl          *readline.Instance
	verbose     bool
	autoApprove *bool
	spinner     *term.Spinner
}

func (t *terminalChatIO) OnThinkingStart(round int) {
	t.spinner = startSpinner(fmt.Sprintf("[round %d] ⟳ thinking...", round))
}

func (t *terminalChatIO) OnThinkingDone(round int, elapsed time.Duration) {
	if t.spinner != nil {
		t.spinner.Stop()
		t.spinner = nil
	}
	printThinkingDone(round, elapsed)
}

func (t *terminalChatIO) OnReasoning(text string) {
	printAIReasoning(text)
}

func (t *terminalChatIO) OnToolStart(name, argsDisplay string) {
	if name == "exec_shell" {
		fmt.Printf("  %s▶ exec_shell%s %s%s%s\n", colorYellow, colorReset, colorGray, argsDisplay, colorReset)
		return
	}
	printToolStart(name, argsDisplay)
}

func (t *terminalChatIO) OnToolDone(name, argsDisplay string, elapsed time.Duration, resultLen int, isErr bool) {
	if name == "exec_shell" {
		return
	}
	printToolDone(name, argsDisplay, elapsed, resultLen, isErr)
}

func (t *terminalChatIO) OnToolOutput(result string) {
	if t.verbose {
		printToolOutput(result, 5)
	}
}

func (t *terminalChatIO) ApproveShell(command string) (bool, string) {
	fmt.Printf("\n\033[33m! AI requests command:\033[0m %s\n", command)
	
	if *t.autoApprove {
		fmt.Printf("\033[33m  (auto-approved)\033[0m\n")
		return true, ""
	}
	
	defer t.rl.SetPrompt(chatPrompt)
	t.rl.SetPrompt("\033[33mConfirm? [y/n/e(edit)/a(all)]:\033[0m ")
	line, err := t.rl.Readline()
	if err != nil {
		// 捕捉 Ctrl+C (ErrInterrupt) 和 Ctrl+D (EOF)
		if err == readline.ErrInterrupt || err == io.EOF {
			fmt.Println("\n\033[31m操作已取消，程序退出。\033[0m")
			os.Exit(0) // 强制中断退出程序
		}
		return false, ""
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	
	switch answer {
	case "y", "yes":
		return true, ""
	case "a", "all":
		*t.autoApprove = true
		return true, ""
	case "e", "edit":
		t.rl.SetPrompt("\033[33mEnter modified command:\033[0m ")
		edited, err := t.rl.Readline()
		if err != nil {
			// 在编辑模式下按 Ctrl+C 也需要处理
			if err == readline.ErrInterrupt || err == io.EOF {
				fmt.Println("\n\033[31m操作已取消，程序退出。\033[0m")
				os.Exit(0)
			}
			return false, ""
		}
		cmd := strings.TrimSpace(edited)
		if cmd == "" {
			return false, ""
		}
		fmt.Printf("\033[33mWill execute:\033[0m %s\n", cmd)
		return true, cmd
	default:
		return false, ""
	}
}
