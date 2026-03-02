package chat

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/cprobe/catpaw/pkg/cmdx"
)

const (
	maxShellOutputBytes  = 4 * 1024   // 4KB sent to AI
	maxShellCaptureBytes = 256 * 1024 // 256KB captured from process
)

// cappedWriter drops writes after the internal buffer reaches its cap.
// All Write calls report success so the child process never gets EPIPE.
type cappedWriter struct {
	buf bytes.Buffer
	cap int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	remaining := w.cap - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *cappedWriter) String() string { return w.buf.String() }

// execShellInteractive prompts the user for confirmation, then executes
// the command via /bin/sh -c. Returns the output or a rejection message.
func execShellInteractive(ctx context.Context, rl *readline.Instance, command string, timeout time.Duration) (string, error) {
	defer rl.SetPrompt(chatPrompt)

	fmt.Printf("\n\033[33m! AI requests command:\033[0m %s\n", command)
	rl.SetPrompt("\033[33mConfirm? [y/n/e(edit)]:\033[0m ")
	line, err := rl.Readline()
	if err != nil {
		return "user rejected command execution", nil
	}
	answer := strings.TrimSpace(strings.ToLower(line))

	switch answer {
	case "y", "yes":
		// proceed
	case "e", "edit":
		rl.SetPrompt("\033[33mEnter modified command:\033[0m ")
		edited, err := rl.Readline()
		if err != nil {
			return "user cancelled command execution", nil
		}
		command = strings.TrimSpace(edited)
		if command == "" {
			return "user cancelled command execution", nil
		}
		fmt.Printf("\033[33mWill execute:\033[0m %s\n", command)
	default:
		return "user rejected command execution", nil
	}

	if err := ctx.Err(); err != nil {
		return "context cancelled, command not executed", nil
	}

	cmd := exec.Command("/bin/sh", "-c", command)
	stdout := &cappedWriter{cap: maxShellCaptureBytes}
	stderr := &cappedWriter{cap: maxShellCaptureBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmdx.CmdStart(cmd); err != nil {
		return fmt.Sprintf("[exit] %v", err), nil
	}

	runErr, timedOut := cmdx.CmdWait(cmd, timeout)
	if timedOut {
		return fmt.Sprintf("command timed out (%v), process killed", timeout), nil
	}

	output := stdout.String()
	if errOut := stderr.String(); errOut != "" {
		if output != "" {
			output += "\n"
		}
		output += "[stderr] " + errOut
	}
	if runErr != nil {
		output += "\n[exit] " + runErr.Error()
	}
	return truncateShellOutput(output), nil
}

func truncateShellOutput(s string) string {
	if len(s) <= maxShellOutputBytes {
		return s
	}
	return s[:maxShellOutputBytes] + "\n...[output truncated]"
}
