package systemd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
)

const timersTimeout = 10 * time.Second
const timersMaxOutput = 128 * 1024

func registerTimers(registry *diagnose.ToolRegistry) {
	registry.Register("systemd", diagnose.DiagnoseTool{
		Name:        "systemd_timers",
		Description: "List systemd timers with next/last trigger times and activation status. Highlights overdue timers.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "show_all", Type: "string", Description: "Set to 'true' to include inactive timers (default: active only)"},
		},
		Execute: execTimers,
	})
}

type timerEntry struct {
	next      string
	left      string
	last      string
	passed    string
	unit      string
	activates string
}

func (t *timerEntry) isOverdue() bool {
	return strings.Contains(strings.ToLower(t.passed), "pass") ||
		strings.HasPrefix(strings.TrimSpace(t.left), "-")
}

func execTimers(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("systemd_timers requires linux (current: %s)", runtime.GOOS)
	}

	showAll := strings.ToLower(strings.TrimSpace(args["show_all"])) == "true"

	raw, err := runListTimers(ctx, showAll)
	if err != nil {
		return "", err
	}

	entries := parseTimerOutput(raw)
	return formatTimers(entries, showAll), nil
}

func runListTimers(ctx context.Context, showAll bool) (string, error) {
	sctl, err := exec.LookPath("systemctl")
	if err != nil {
		return "", fmt.Errorf("systemctl not found: %w", err)
	}

	cmdArgs := []string{"list-timers", "--no-pager", "--no-legend"}
	if showAll {
		cmdArgs = append(cmdArgs, "--all")
	}

	outBuf := &cappedWriter{buf: bytes.NewBuffer(make([]byte, 0, 4096)), max: timersMaxOutput}
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, sctl, cmdArgs...)
	cmd.Stdout = outBuf
	cmd.Stderr = &errBuf

	if err, _ := cmdx.RunTimeout(cmd, timersTimeout); err != nil {
		return "", fmt.Errorf("systemctl list-timers: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}

	return outBuf.buf.String(), nil
}

func parseTimerOutput(raw string) []timerEntry {
	var entries []timerEntry

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "NEXT") || strings.HasSuffix(line, "timers listed.") {
			continue
		}

		entry := parseTimerLine(line)
		if entry.unit != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// parseTimerLine parses a line from `systemctl list-timers --no-legend`.
// Format varies by systemd version. We parse using a heuristic: find .timer unit names.
func parseTimerLine(line string) timerEntry {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return timerEntry{}
	}

	timerIdx := -1
	activatesIdx := -1
	for i, f := range fields {
		if strings.HasSuffix(f, ".timer") {
			timerIdx = i
		}
		if strings.HasSuffix(f, ".service") || strings.HasSuffix(f, ".target") || strings.HasSuffix(f, ".path") {
			activatesIdx = i
		}
	}

	if timerIdx < 0 {
		return timerEntry{}
	}

	entry := timerEntry{unit: fields[timerIdx]}
	if activatesIdx >= 0 {
		entry.activates = fields[activatesIdx]
	}

	beforeTimer := fields[:timerIdx]
	entry.next, entry.left, entry.last, entry.passed = classifyTimerFields(beforeTimer)

	return entry
}

// classifyTimerFields tries to extract timing info from the fields before the .timer unit name.
// systemd output typically has: NEXT LEFT LAST PASSED
// where NEXT/LAST can be multi-word timestamps or "n/a".
func classifyTimerFields(fields []string) (next, left, last, passed string) {
	if len(fields) == 0 {
		return
	}

	text := strings.Join(fields, " ")

	if strings.Contains(text, "n/a") || strings.Contains(text, "left") ||
		strings.Contains(text, "ago") || strings.Contains(text, "passed") {
		parts := splitTimerBlocks(text)
		switch len(parts) {
		case 4:
			return parts[0], parts[1], parts[2], parts[3]
		case 3:
			return parts[0], parts[1], parts[2], ""
		case 2:
			return parts[0], parts[1], "", ""
		case 1:
			return parts[0], "", "", ""
		}
	}

	return text, "", "", ""
}

// splitTimerBlocks splits timing fields by known delimiters:
// timestamps end with timezone like "UTC", "CST", etc.
// relative times end with "left", "ago", "passed"
// "n/a" stands alone
func splitTimerBlocks(text string) []string {
	var blocks []string
	var current strings.Builder

	words := strings.Fields(text)
	for _, w := range words {
		current.WriteString(w)
		current.WriteString(" ")

		lower := strings.ToLower(w)
		if lower == "left" || lower == "ago" || lower == "passed" ||
			lower == "n/a" || isTimezone(w) {
			blocks = append(blocks, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}
	if current.Len() > 0 {
		blocks = append(blocks, strings.TrimSpace(current.String()))
	}
	return blocks
}

func isTimezone(s string) bool {
	if len(s) < 1 || len(s) > 5 {
		return false
	}
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func formatTimers(entries []timerEntry, showAll bool) string {
	if len(entries) == 0 {
		scope := "active"
		if showAll {
			scope = ""
		}
		return fmt.Sprintf("No %s systemd timers found.\n", scope)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Systemd Timers: %d entries\n\n", len(entries))

	overdueCount := 0
	for _, e := range entries {
		marker := ""
		if e.isOverdue() {
			marker = " [!]"
			overdueCount++
		}

		fmt.Fprintf(&b, "  Timer:     %s%s\n", e.unit, marker)
		if e.activates != "" {
			fmt.Fprintf(&b, "  Activates: %s\n", e.activates)
		}
		if e.next != "" {
			fmt.Fprintf(&b, "  Next:      %s", e.next)
			if e.left != "" {
				fmt.Fprintf(&b, "  (%s)", e.left)
			}
			b.WriteString("\n")
		}
		if e.last != "" {
			fmt.Fprintf(&b, "  Last:      %s", e.last)
			if e.passed != "" {
				fmt.Fprintf(&b, "  (%s)", e.passed)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if overdueCount > 0 {
		fmt.Fprintf(&b, "[!] %d timer(s) appear overdue\n", overdueCount)
	}

	return b.String()
}

type cappedWriter struct {
	buf *bytes.Buffer
	n   int
	max int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	remain := w.max - w.n
	if remain <= 0 {
		return len(p), nil
	}
	if len(p) > remain {
		p = p[:remain]
	}
	n, err := w.buf.Write(p)
	w.n += n
	return n, err
}
