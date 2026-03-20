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

const dmesgTimeout = 10 * time.Second

func registerDmesg(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_kernel", "sysdiag:kernel",
		"Kernel diagnostic tools (dmesg). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_kernel", diagnose.DiagnoseTool{
		Name:        "dmesg_recent",
		Description: "Show recent kernel messages (OOM kills, hardware errors, etc). Default: last 5 minutes, warn+ level.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "since", Type: "string", Description: "Time window (e.g. '5m', '1h'). Default: 5m"},
			{Name: "level", Type: "string", Description: "Minimum level: emerg,alert,crit,err,warn,notice,info,debug. Default: warn"},
			{Name: "max_lines", Type: "string", Description: "Max output lines. Default: 50"},
		},
		Execute: execDmesgRecent,
	})
}

func execDmesgRecent(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("dmesg_recent requires linux (current: %s)", runtime.GOOS)
	}

	since := "5m"
	if s := args["since"]; s != "" {
		since = s
	}
	sinceSeconds, err := parseDurationToSeconds(since)
	if err != nil {
		return "", fmt.Errorf("invalid since %q: %w", since, err)
	}

	level := "warn"
	if l := args["level"]; l != "" {
		level = strings.ToLower(l)
	}
	if !isValidDmesgLevel(level) {
		return "", fmt.Errorf("invalid level %q (valid: emerg,alert,crit,err,warn,notice,info,debug)", level)
	}

	maxLines := 50
	if s := args["max_lines"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid max_lines %q: must be a positive integer", s)
		}
		if n > 500 {
			n = 500
		}
		maxLines = n
	}

	bin, err := exec.LookPath("dmesg")
	if err != nil {
		return "", fmt.Errorf("dmesg not found: %w", err)
	}

	timeout := dmesgTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	cutoff := time.Now().Add(-time.Duration(sinceSeconds) * time.Second)

	output, err := tryDmesg(bin, timeout,
		"--time-format=iso", "--level="+level, fmt.Sprintf("--since=-%ds", sinceSeconds))
	if err != nil {
		// Fallback: old util-linux without --since / --time-format
		output, err = tryDmesg(bin, timeout, "-T", "--level="+level)
		if err != nil {
			// Last resort: very old dmesg without --level
			output, err = tryDmesg(bin, timeout, "-T")
			if err != nil {
				return "", fmt.Errorf("dmesg failed: %w", err)
			}
		}
		output = filterDmesgByTime(output, cutoff)
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return fmt.Sprintf("No kernel messages at level >=%s in the last %s.", level, since), nil
	}

	if len(lines) > maxLines {
		truncated := len(lines) - maxLines
		lines = lines[len(lines)-maxLines:]
		var b strings.Builder
		fmt.Fprintf(&b, "...[%d earlier messages truncated]\n", truncated)
		b.WriteString(strings.Join(lines, "\n"))
		return b.String(), nil
	}

	return strings.Join(lines, "\n"), nil
}

func tryDmesg(bin string, timeout time.Duration, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("dmesg timed out after %s", timeout)
	}

	output := stdout.String()
	if runErr != nil && output == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return "", fmt.Errorf("%v (%s)", runErr, stderrStr)
		}
		return "", runErr
	}
	return output, nil
}

// filterDmesgByTime keeps only lines whose leading timestamp is after cutoff.
// Handles "dmesg -T" format: "[Wed Jan  1 12:34:56 2025] message"
func filterDmesgByTime(output string, cutoff time.Time) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		ts := parseDmesgTimestamp(line)
		if ts.IsZero() || ts.After(cutoff) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

var dmesgTimeLayouts = []string{
	"Mon Jan  2 15:04:05 2006",
	"Mon Jan 2 15:04:05 2006",
}

func parseDmesgTimestamp(line string) time.Time {
	start := strings.IndexByte(line, '[')
	end := strings.IndexByte(line, ']')
	if start < 0 || end < 0 || end <= start+1 {
		return time.Time{}
	}
	raw := strings.TrimSpace(line[start+1 : end])
	for _, layout := range dmesgTimeLayouts {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

const maxDmesgSeconds = 7 * 86400 // 7 days

func parseDurationToSeconds(s string) (int, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	sec := int(d.Seconds())
	if sec <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	if sec > maxDmesgSeconds {
		return 0, fmt.Errorf("duration exceeds maximum (7 days)")
	}
	return sec, nil
}

var validDmesgLevels = map[string]bool{
	"emerg": true, "alert": true, "crit": true, "err": true,
	"warn": true, "notice": true, "info": true, "debug": true,
}

func isValidDmesgLevel(level string) bool {
	return validDmesgLevels[level]
}
