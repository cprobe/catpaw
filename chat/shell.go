package chat

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/cprobe/catpaw/diagnose"
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

// execShell executes a shell command via /bin/sh -c and returns the output.
// Approval must be handled by the caller before invoking this function.
func execShell(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("context cancelled, command not executed")
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
	return diagnose.TruncateUTF8(s, maxShellOutputBytes) + "\n...[output truncated]"
}
