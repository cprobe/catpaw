package systemd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
)

const (
	defaultJournalLines = 50
	maxJournalLines     = 500
)

// Valid journalctl priority levels (syslog).
var validPriorities = map[string]bool{
	"emerg": true, "alert": true, "crit": true, "err": true,
	"warning": true, "notice": true, "info": true, "debug": true,
	"0": true, "1": true, "2": true, "3": true,
	"4": true, "5": true, "6": true, "7": true,
}

// reSinceFormat validates the --since parameter to prevent injection.
// Accepts: "1h ago", "5m ago", "today", "yesterday", "2026-03-01", "2026-03-01 10:00:00", etc.
var reSinceFormat = regexp.MustCompile(`^[\d\-T: ]+$|^\d+[smhd] ago$|^(today|yesterday)$`)

func registerJournalQuery(registry *diagnose.ToolRegistry) {
	registry.Register("systemd", diagnose.DiagnoseTool{
		Name:        "journal_query",
		Description: "Query systemd journal logs for a specific unit. Shows recent log entries with timestamp and priority.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "unit", Type: "string", Description: "Systemd unit name (e.g. 'nginx', 'sshd.service')", Required: true},
			{Name: "since", Type: "string", Description: "Time filter (e.g. '5m ago', '1h ago', 'today', '2026-03-01'). Default: 1h ago"},
			{Name: "priority", Type: "string", Description: "Minimum priority: emerg,alert,crit,err,warning,notice,info,debug (or 0-7). Default: info"},
			{Name: "max_lines", Type: "string", Description: "Max log lines (default 50, max 500)"},
		},
		Execute: execJournalQuery,
	})
}

func execJournalQuery(ctx context.Context, args map[string]string) (string, error) {
	unit := strings.TrimSpace(args["unit"])
	if unit == "" {
		return "", fmt.Errorf("unit parameter is required")
	}
	if strings.ContainsAny(unit, "\n\r\t;|&$`") || len(unit) > 256 {
		return "", fmt.Errorf("invalid unit name %q", unit)
	}
	unit = normalizeUnitName(unit)

	since := "1h ago"
	if s := args["since"]; s != "" {
		if !reSinceFormat.MatchString(s) {
			return "", fmt.Errorf("invalid since format %q (use e.g. '5m ago', '1h ago', 'today', '2026-03-01')", s)
		}
		since = s
	}

	priority := "info"
	if p := args["priority"]; p != "" {
		pl := strings.ToLower(p)
		if !validPriorities[pl] {
			return "", fmt.Errorf("invalid priority %q (valid: emerg,alert,crit,err,warning,notice,info,debug or 0-7)", p)
		}
		priority = pl
	}

	maxLines := defaultJournalLines
	if s := args["max_lines"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid max_lines %q: must be a positive integer", s)
		}
		if n > maxJournalLines {
			n = maxJournalLines
		}
		maxLines = n
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("journal_query requires linux (current: %s)", runtime.GOOS)
	}

	return runJournalctl(ctx, unit, since, priority, maxLines)
}

func runJournalctl(ctx context.Context, unit, since, priority string, maxLines int) (string, error) {
	bin, err := exec.LookPath("journalctl")
	if err != nil {
		return "", fmt.Errorf("journalctl not found: %w", err)
	}

	cmdArgs := []string{
		"-u", unit,
		"--since", since,
		"--priority", priority,
		"--no-pager",
		"--output", "short-iso",
		"-n", strconv.Itoa(maxLines),
	}

	cmd := exec.Command(bin, cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := effectiveTimeout(ctx, diagnoseTimeout)
	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("journalctl timed out after %s", timeout)
	}

	output := strings.TrimSpace(stdout.String())
	if runErr != nil && output == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return "", fmt.Errorf("journalctl failed: %v (%s)", runErr, stderrStr)
		}
		return "", fmt.Errorf("journalctl failed: %v", runErr)
	}

	if output == "" || output == "-- No entries --" {
		return fmt.Sprintf("No journal entries for %s since %s at priority >=%s.", unit, since, priority), nil
	}

	lines := strings.Split(output, "\n")
	headerLine := ""
	// journalctl short-iso may include a "-- Logs begin at ..." header line
	if len(lines) > 0 && strings.HasPrefix(lines[0], "-- ") {
		headerLine = lines[0]
		lines = lines[1:]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Journal for %s (since %s, priority >=%s): %d entries\n\n", unit, since, priority, len(lines))
	if headerLine != "" {
		b.WriteString(headerLine)
		b.WriteByte('\n')
	}
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}
