package sysdiag

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/pkg/cmdx"
)

const (
	oomTimeout       = 15 * time.Second
	maxOOMItems      = 50
	maxFallbackBytes = 2 * 1024 * 1024 // 2MB cap for fallback dmesg to avoid OOM
)

func registerOOM(registry *diagnose.ToolRegistry) {
	registry.Register("sysdiag_kernel", diagnose.DiagnoseTool{
		Name:        "oom_history",
		Description: "Parse recent OOM Kill events from kernel log. Shows killed process, PID, memory usage, and OOM score for each event.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "since", Type: "string", Description: "Time window (e.g. '1h', '24h', '7d'). Default: 24h. Max: 7d."},
			{Name: "max_events", Type: "string", Description: "Max OOM events to return (default 20, max 50)"},
		},
		Execute: execOOMHistory,
	})
}

// oomEvent holds parsed data from a single OOM kill kernel message.
type oomEvent struct {
	timestamp string
	killed    string
	pid       int
	uid       int
	rssPages  int64
	score     int
	extra     string // the raw "Killed process ..." line for context
}

// Regex patterns for OOM kill lines across different kernel versions.
//
// Kernel 3.x-4.x: "Killed process 1234 (nginx) total-vm:..., anon-rss:..., file-rss:..."
// Kernel 5.x:     "Killed process 1234 (nginx), UID 0, total-vm:..., anon-rss:..., file-rss:..."
// Kernel 6.x:     "Killed process 1234 (nginx) total-vm:..., anon-rss:..., file-rss:..., shmem-rss:..., UID:1011 ..."
var (
	// Captures: pid(1), name(2), uid_old(3), total-vm(4), anon-rss(5), file-rss(6), shmem-rss(7), uid_new(8)
	reKilledProcess = regexp.MustCompile(
		`Killed process (\d+) \(([^)]+)\)(?:,? UID:? ?(\d+))?,? total-vm:(\d+)kB, anon-rss:(\d+)kB, file-rss:(\d+)kB(?:, shmem-rss:(\d+)kB)?(?:, UID:? ?(\d+))?`)

	reOOMScore = regexp.MustCompile(
		`oom_score_adj=(-?\d+)`)

	reTimestamp = regexp.MustCompile(
		`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?|^\[?\s*[\d.]+\]?\s*`)
)

func execOOMHistory(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("oom_history requires linux (current: %s)", runtime.GOOS)
	}

	since := "24h"
	if s := args["since"]; s != "" {
		since = s
	}
	sinceSeconds, err := parseDurationToSeconds(since)
	if err != nil {
		return "", fmt.Errorf("invalid since %q: %w", since, err)
	}

	maxEvents := 20
	if s := args["max_events"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid max_events %q: must be a positive integer", s)
		}
		if n > maxOOMItems {
			n = maxOOMItems
		}
		maxEvents = n
	}

	raw, err := readDmesgForOOM(ctx, sinceSeconds)
	if err != nil {
		return "", err
	}

	events := parseOOMEvents(raw)

	if len(events) == 0 {
		return fmt.Sprintf("No OOM kill events found in the last %s.", since), nil
	}

	total := len(events)
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}

	return formatOOMEvents(events, total, since), nil
}

func readDmesgForOOM(ctx context.Context, sinceSeconds int) (string, error) {
	bin, err := exec.LookPath("dmesg")
	if err != nil {
		return "", fmt.Errorf("dmesg not found: %w", err)
	}

	cmdArgs := []string{
		"--time-format=iso",
		"--since=" + fmt.Sprintf("-%ds", sinceSeconds),
	}

	timeout := oomTimeout
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
		return "", fmt.Errorf("dmesg timed out after %s", timeout)
	}

	output := stdout.String()
	if runErr != nil && output == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		// Fallback: try without --since and --time-format (older dmesg/util-linux)
		if strings.Contains(stderrStr, "since") || strings.Contains(stderrStr, "invalid option") ||
			strings.Contains(stderrStr, "time-format") {
			return readDmesgFallback(ctx, timeout, sinceSeconds)
		}
		if stderrStr != "" {
			return "", fmt.Errorf("dmesg failed: %v (%s)", runErr, stderrStr)
		}
		return "", fmt.Errorf("dmesg failed: %v", runErr)
	}
	return output, nil
}

// readDmesgFallback reads dmesg without --since/--time-format for older util-linux.
// Uses plain dmesg; output is truncated to avoid excessive memory use.
func readDmesgFallback(_ context.Context, timeout time.Duration, _ int) (string, error) {
	bin, err := exec.LookPath("dmesg")
	if err != nil {
		return "", fmt.Errorf("dmesg not found: %w", err)
	}
	cmd := exec.Command(bin) // no args: older dmesg may not support --time-format or --since
	var stdout strings.Builder
	cmd.Stdout = &stdout

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("dmesg timed out")
	}

	output := stdout.String()
	if runErr != nil && output == "" {
		return "", fmt.Errorf("dmesg failed: %v", runErr)
	}
	// Cap output size; fallback returns full log, cannot filter by --since
	if len(output) > maxFallbackBytes {
		output = output[len(output)-maxFallbackBytes:]
		// Start at next newline to avoid cutting a line
		if i := strings.Index(output, "\n"); i >= 0 {
			output = output[i+1:]
		}
	}
	return output, nil
}

func parseOOMEvents(raw string) []oomEvent {
	// Sanitize: dmesg may produce non-UTF8 (e.g. binary, other encodings)
	raw = strings.ToValidUTF8(raw, "")

	s := bufio.NewScanner(strings.NewReader(raw))
	s.Buffer(nil, 256*1024) // 256KB per line for long dmesg lines
	events := make([]oomEvent, 0, 16)
	var lastScore int

	for s.Scan() {
		line := s.Text()
		// Track oom_score_adj from the summary lines before the kill
		if m := reOOMScore.FindStringSubmatch(line); len(m) > 1 {
			if v, err := strconv.Atoi(m[1]); err == nil {
				lastScore = v
			}
		}

		m := reKilledProcess.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		ev := oomEvent{
			timestamp: extractTimestamp(line),
			extra:     trimToMaxLen(line, 200),
			score:     lastScore,
		}

		if v, err := strconv.Atoi(m[1]); err == nil {
			ev.pid = v
		}
		ev.killed = m[2]
		// UID: m[3]=old format (before total-vm), m[8]=kernel 6.x (after shmem-rss)
		for _, idx := range []int{8, 3} {
			if idx < len(m) && m[idx] != "" {
				if v, err := strconv.Atoi(m[idx]); err == nil {
					ev.uid = v
					break
				}
			}
		}
		// RSS = anon + file + shmem (all in kB); store as pages (4KB) for display
		var anon, file, shmem int64
		if v, err := strconv.ParseInt(m[5], 10, 64); err == nil {
			anon = v
		}
		if v, err := strconv.ParseInt(m[6], 10, 64); err == nil {
			file = v
		}
		if len(m) > 7 && m[7] != "" {
			if v, err := strconv.ParseInt(m[7], 10, 64); err == nil {
				shmem = v
			}
		}
		ev.rssPages = (anon + file + shmem) / 4
		if ev.rssPages < 0 {
			ev.rssPages = 0
		}

		events = append(events, ev)
		lastScore = 0
	}
	return events
}

func extractTimestamp(line string) string {
	m := reTimestamp.FindString(line)
	if m != "" {
		return strings.TrimSpace(m)
	}
	return "?"
}

func trimToMaxLen(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	n := 0
	for i := range s {
		if n >= maxLen {
			return s[:i] + "..."
		}
		n++
	}
	return s
}

func formatOOMEvents(events []oomEvent, total int, since string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "OOM Kill events in last %s: %d total", since, total)
	if total > len(events) {
		fmt.Fprintf(&b, " (showing last %d)", len(events))
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "%-20s  %7s  %-20s  %10s  %6s\n",
		"TIME", "PID", "PROCESS", "RSS(KB)", "SCORE")
	fmt.Fprintf(&b, "%-20s  %7s  %-20s  %10s  %6s\n",
		strings.Repeat("-", 20), "-------", strings.Repeat("-", 20),
		"----------", "------")

	for _, ev := range events {
		rssKB := ev.rssPages * 4
		fmt.Fprintf(&b, "%-20s  %7d  %-20s  %10d  %6d\n",
			ev.timestamp, ev.pid, truncName(ev.killed, 20), rssKB, ev.score)
	}
	return b.String()
}

func truncName(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	n := 0
	for i := range s {
		n++
		if n >= maxLen {
			return s[:i] + "~"
		}
	}
	return s
}
