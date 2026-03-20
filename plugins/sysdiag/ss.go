package sysdiag

import (
	"bytes"
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

const (
	ssTimeout     = 15 * time.Second
	defaultSSMax  = 50
	maxSSMax      = 500
	ssMaxOutput   = 64 * 1024 // cap raw ss output
)

// Valid TCP states for filtering.
var validTCPStates = map[string]bool{
	"established": true, "syn-sent": true, "syn-recv": true,
	"fin-wait-1": true, "fin-wait-2": true, "time-wait": true,
	"close": true, "close-wait": true, "last-ack": true,
	"listen": true, "closing": true, "all": true,
}

func registerSS(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_ss", "sysdiag:ss",
		"Socket detail tools (TCP connection info with queues, RTT, retransmits). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_ss", diagnose.DiagnoseTool{
		Name:        "ss_detail",
		Description: "Show detailed TCP socket info: Send-Q, Recv-Q, RTT, congestion window, retransmits. Uses 'ss -tinp'. Requires root for full process info.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "state", Type: "string", Description: "TCP state filter (established, listen, close-wait, time-wait, all). Default: established"},
			{Name: "port", Type: "string", Description: "Filter by local or remote port number"},
			{Name: "max_lines", Type: "string", Description: "Max connections to show (default 50, max 500)"},
		},
		Execute: execSSDetail,
	})
}

func execSSDetail(ctx context.Context, args map[string]string) (string, error) {
	state := "established"
	if s := args["state"]; s != "" {
		sl := strings.ToLower(s)
		if !validTCPStates[sl] {
			return "", fmt.Errorf("invalid state %q (valid: established, syn-sent, syn-recv, fin-wait-1, fin-wait-2, time-wait, close, close-wait, last-ack, listen, closing, all)", s)
		}
		state = sl
	}

	port := ""
	if p := args["port"]; p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return "", fmt.Errorf("invalid port %q: must be 1-65535", p)
		}
		port = p
	}

	maxLines := defaultSSMax
	if s := args["max_lines"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid max_lines %q: must be a positive integer", s)
		}
		if n > maxSSMax {
			n = maxSSMax
		}
		maxLines = n
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("ss_detail requires linux (current: %s)", runtime.GOOS)
	}

	return runSS(ctx, state, port, maxLines)
}

func runSS(ctx context.Context, state, port string, maxLines int) (string, error) {
	bin, err := exec.LookPath("ss")
	if err != nil {
		return "", fmt.Errorf("ss not found: %w", err)
	}

	// -t: TCP only
	// -i: internal TCP info (RTT, cwnd, retransmits)
	// -n: numeric (no DNS resolution)
	// -p: show process (needs root)
	cmdArgs := []string{"-tinp"}

	if state != "all" {
		cmdArgs = append(cmdArgs, "state", state)
	}

	if port != "" {
		cmdArgs = append(cmdArgs, "(", "sport", "=", ":"+port, "or", "dport", "=", ":"+port, ")")
	}

	cmd := exec.Command(bin, cmdArgs...)
	var stdout bytes.Buffer
	cmd.Stdout = &cappedBuf{buf: &stdout, max: ssMaxOutput + 1024}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	timeout := ssTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("ss timed out after %s", timeout)
	}

	output := stdout.String()
	if runErr != nil && output == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return "", fmt.Errorf("ss failed: %v (%s)", runErr, stderrStr)
		}
		return "", fmt.Errorf("ss failed: %v", runErr)
	}

	// Cap output size
	if len(output) > ssMaxOutput {
		output = output[:ssMaxOutput]
		if i := strings.LastIndex(output, "\n"); i >= 0 {
			output = output[:i]
		}
		output += "\n...[output truncated]"
	}

	return formatSSOutput(output, state, port, maxLines), nil
}

func formatSSOutput(raw, state, port string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		filter := state
		if port != "" {
			filter += " port=" + port
		}
		return fmt.Sprintf("No TCP connections matching: %s", filter)
	}

	// First line is the header from ss
	header := ""
	dataLines := lines
	if len(lines) > 0 && (strings.HasPrefix(lines[0], "State") || strings.HasPrefix(lines[0], "Recv-Q")) {
		header = lines[0]
		dataLines = lines[1:]
	}

	// ss -i outputs multi-line per connection (main line + indented detail lines)
	// Group them: a non-indented line starts a new connection
	connections := groupSSConnections(dataLines)
	total := len(connections)

	if total > maxLines {
		connections = connections[:maxLines]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "TCP connections (state=%s", state)
	if port != "" {
		fmt.Fprintf(&b, ", port=%s", port)
	}
	fmt.Fprintf(&b, "): %d", total)
	if total > maxLines {
		fmt.Fprintf(&b, " (showing first %d)", maxLines)
	}
	b.WriteString("\n\n")

	if header != "" {
		b.WriteString(header)
		b.WriteByte('\n')
	}
	for _, conn := range connections {
		for _, line := range conn {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// cappedBuf limits the amount of data written to avoid unbounded memory on large ss output.
type cappedBuf struct {
	buf *bytes.Buffer
	n   int
	max int
}

func (w *cappedBuf) Write(p []byte) (int, error) {
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

// groupSSConnections groups ss output lines into per-connection groups.
// Lines starting with whitespace or tab are continuation of the previous connection.
func groupSSConnections(lines []string) [][]string {
	var groups [][]string
	var current []string

	for _, line := range lines {
		if line == "" {
			continue
		}
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if current != nil {
				current = append(current, line)
			} else {
				current = []string{line}
			}
		} else {
			if current != nil {
				groups = append(groups, current)
			}
			current = []string{line}
		}
	}
	if current != nil {
		groups = append(groups, current)
	}
	return groups
}
