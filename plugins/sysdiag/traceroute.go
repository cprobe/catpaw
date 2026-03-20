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

const tracerouteTimeout = 30 * time.Second

func registerTraceroute(registry *diagnose.ToolRegistry) {
	registry.Register("sysdiag_net", diagnose.DiagnoseTool{
		Name:        "traceroute",
		Description: "Trace network route to a host. Uses traceroute on Linux/macOS, tracert on Windows.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "host", Type: "string", Description: "Destination host or IP", Required: true},
			{Name: "max_hops", Type: "string", Description: "Maximum number of hops (default 15, max 30)"},
		},
		Execute: execTraceroute,
	})
}

func execTraceroute(ctx context.Context, args map[string]string) (string, error) {
	host := strings.TrimSpace(args["host"])
	if host == "" {
		return "", fmt.Errorf("host parameter is required")
	}

	if strings.ContainsAny(host, " \t;|&$`") {
		return "", fmt.Errorf("invalid host %q: contains disallowed characters", host)
	}

	maxHops := 15
	if s := args["max_hops"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid max_hops %q: must be a positive integer", s)
		}
		if n > 30 {
			n = 30
		}
		maxHops = n
	}

	var bin string
	var cmdArgs []string
	var lookupName string

	switch runtime.GOOS {
	case "windows":
		lookupName = "tracert"
	default:
		lookupName = "traceroute"
	}

	var err error
	bin, err = exec.LookPath(lookupName)
	if err != nil {
		return "", fmt.Errorf("%s not found: %w", lookupName, err)
	}

	switch runtime.GOOS {
	case "windows":
		cmdArgs = []string{"-h", strconv.Itoa(maxHops), "-d", host}
	default:
		cmdArgs = []string{"-m", strconv.Itoa(maxHops), "-n", "-w", "2", host}
	}

	timeout := tracerouteTimeout
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
		output := strings.TrimRight(stdout.String(), "\n")
		if output != "" {
			return output + "\n\n...[timed out, partial results shown]", nil
		}
		return "", fmt.Errorf("%s timed out after %s", lookupName, timeout)
	}

	output := stdout.String()
	if runErr != nil && output == "" {
		return "", fmt.Errorf("%s failed: %v (stderr: %s)", lookupName, runErr, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimRight(output, "\n"), nil
}
