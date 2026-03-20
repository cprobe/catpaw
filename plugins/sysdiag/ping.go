package sysdiag

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
)

const pingTimeout = 15 * time.Second

func registerPing(registry *diagnose.ToolRegistry) {
	registry.Register("sysdiag_net", diagnose.DiagnoseTool{
		Name:        "ping_check",
		Description: "Ping a host to check network connectivity (uses system ping command)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "host", Type: "string", Description: "Host or IP to ping", Required: true},
			{Name: "count", Type: "string", Description: "Number of ping packets (default 3, max 10)"},
		},
		Execute: execPingCheck,
	})
}

func execPingCheck(ctx context.Context, args map[string]string) (string, error) {
	host := strings.TrimSpace(args["host"])
	if host == "" {
		return "", fmt.Errorf("host parameter is required")
	}

	if strings.ContainsAny(host, " \t;|&$`") {
		return "", fmt.Errorf("invalid host %q: contains disallowed characters", host)
	}

	count := 3
	if s := args["count"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid count %q: must be a positive integer", s)
		}
		if n > 10 {
			n = 10
		}
		count = n
	}

	bin, err := exec.LookPath("ping")
	if err != nil {
		return "", fmt.Errorf("ping not found: %w", err)
	}

	var cmdArgs []string
	switch runtime.GOOS {
	case "windows":
		cmdArgs = []string{"-n", strconv.Itoa(count), host}
	default:
		cmdArgs = []string{"-c", strconv.Itoa(count), "-W", "3", host}
	}

	timeout := pingTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	cmd := exec.Command(bin, cmdArgs...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("ping timed out after %s", timeout)
	}

	output := stdout.String()
	if runErr != nil && output == "" {
		return "", fmt.Errorf("ping failed: %v (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimRight(output, "\n"), nil
}
