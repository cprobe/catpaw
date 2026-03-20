package sysdiag

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
)

const (
	listenTimeout   = 10 * time.Second
	listenMaxOutput = 64 * 1024
	netstatPath     = "/proc/net/netstat"
	netstatMaxSize  = 64 * 1024
)

func registerListen(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_listen", "sysdiag:listen",
		"Listen queue diagnostic tools (backlog usage, overflow/drop counters). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_listen", diagnose.DiagnoseTool{
		Name:        "listen_overflow",
		Description: "Show LISTEN sockets with queue usage (Recv-Q/Send-Q backlog), plus kernel ListenOverflows and ListenDrops counters. Highlights sockets with non-empty queues or high backlog usage.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "port", Type: "string", Description: "Filter by local port number (optional)"},
		},
		Execute: execListenOverflow,
	})
}

type listenSocket struct {
	recvQ   int
	sendQ   int
	local   string
	process string
}

func (s *listenSocket) usage() float64 {
	if s.sendQ <= 0 {
		return 0
	}
	return float64(s.recvQ) / float64(s.sendQ) * 100
}

func execListenOverflow(ctx context.Context, args map[string]string) (string, error) {
	port := strings.TrimSpace(args["port"])
	if port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return "", fmt.Errorf("invalid port: %q (must be 1-65535)", port)
		}
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("listen_overflow requires linux (current: %s)", runtime.GOOS)
	}

	sockets, err := getListenSockets(ctx, port)
	if err != nil {
		return "", err
	}

	overflows, drops := readListenOverflowCounters()

	return formatListenOverflow(sockets, overflows, drops), nil
}

func getListenSockets(ctx context.Context, port string) ([]listenSocket, error) {
	ss, err := exec.LookPath("ss")
	if err != nil {
		return nil, fmt.Errorf("ss not found: %w", err)
	}

	cmdArgs := []string{"-tlnp"}
	if port != "" {
		cmdArgs = append(cmdArgs, "sport", "=", ":"+port)
	}

	outBuf := &cappedBuf{buf: bytes.NewBuffer(make([]byte, 0, 4096)), max: listenMaxOutput}
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, ss, cmdArgs...)
	cmd.Stdout = outBuf
	cmd.Stderr = &errBuf

	if err, _ := cmdx.RunTimeout(cmd, listenTimeout); err != nil {
		return nil, fmt.Errorf("ss -tlnp: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}

	return parseSSListenOutput(outBuf.buf.String()), nil
}

func parseSSListenOutput(raw string) []listenSocket {
	var sockets []listenSocket
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "State") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		recvQ, _ := strconv.Atoi(fields[1])
		sendQ, _ := strconv.Atoi(fields[2])
		local := fields[3]
		process := ""
		for _, f := range fields[4:] {
			if strings.HasPrefix(f, "users:") {
				process = extractProcessName(f)
				break
			}
		}

		sockets = append(sockets, listenSocket{
			recvQ:   recvQ,
			sendQ:   sendQ,
			local:   local,
			process: process,
		})
	}
	return sockets
}

func extractProcessName(usersField string) string {
	start := strings.Index(usersField, "((")
	if start < 0 {
		return ""
	}
	rest := usersField[start+2:]

	end := strings.Index(rest, "\"")
	if end < 0 {
		end = strings.Index(rest, ",")
	}
	if end < 0 {
		return strings.TrimRight(rest, "))")
	}

	name := rest
	if q1 := strings.IndexByte(rest, '"'); q1 >= 0 {
		q2 := strings.IndexByte(rest[q1+1:], '"')
		if q2 >= 0 {
			name = rest[q1+1 : q1+1+q2]
		}
	}
	return name
}

func readListenOverflowCounters() (overflows, drops uint64) {
	f, err := os.Open(netstatPath)
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, netstatMaxSize))
	if err != nil {
		return 0, 0
	}

	lines := strings.Split(string(data), "\n")
	for i := 0; i+1 < len(lines); i += 2 {
		headerLine := lines[i]
		valueLine := lines[i+1]

		if !strings.HasPrefix(headerLine, "TcpExt:") {
			continue
		}
		if !strings.HasPrefix(valueLine, "TcpExt:") {
			continue
		}

		headers := strings.Fields(headerLine)
		values := strings.Fields(valueLine)
		if len(headers) != len(values) {
			continue
		}

		for j := 1; j < len(headers); j++ {
			v, err := strconv.ParseUint(values[j], 10, 64)
			if err != nil {
				continue
			}
			switch headers[j] {
			case "ListenOverflows":
				overflows = v
			case "ListenDrops":
				drops = v
			}
		}
		break
	}
	return
}

func formatListenOverflow(sockets []listenSocket, overflows, drops uint64) string {
	var b strings.Builder

	b.WriteString("Listen Queue Status\n")
	b.WriteString(strings.Repeat("=", 50))
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "Kernel counters (cumulative since boot):\n")
	overflowMark := ""
	if overflows > 0 {
		overflowMark = " [!]"
	}
	dropMark := ""
	if drops > 0 {
		dropMark = " [!]"
	}
	fmt.Fprintf(&b, "  ListenOverflows: %d%s\n", overflows, overflowMark)
	fmt.Fprintf(&b, "  ListenDrops:     %d%s\n", drops, dropMark)

	if overflows > 0 || drops > 0 {
		b.WriteString("  (non-zero means connections were dropped due to full listen queue)\n")
	}
	b.WriteString("\n")

	if len(sockets) == 0 {
		b.WriteString("No LISTEN sockets found.\n")
		return b.String()
	}

	sort.Slice(sockets, func(i, j int) bool {
		return sockets[i].usage() > sockets[j].usage()
	})

	fmt.Fprintf(&b, "LISTEN sockets: %d total\n\n", len(sockets))
	fmt.Fprintf(&b, "%-7s %-7s %-6s %-28s %s\n", "RECV-Q", "SEND-Q", "USE%", "LOCAL", "PROCESS")
	b.WriteString(strings.Repeat("-", 70))
	b.WriteString("\n")

	maxDisplay := 100
	showing := len(sockets)
	if showing > maxDisplay {
		showing = maxDisplay
	}

	queuedCount := 0
	for i, s := range sockets {
		if i >= showing {
			break
		}
		marker := ""
		pct := s.usage()
		if s.recvQ > 0 {
			queuedCount++
			if pct >= 90 {
				marker = " [!!!]"
			} else if pct >= 75 {
				marker = " [!]"
			} else {
				marker = " [*]"
			}
		}

		proc := s.process
		if proc == "" {
			proc = "-"
		}

		fmt.Fprintf(&b, "%-7d %-7d %5.1f%% %-28s %s%s\n",
			s.recvQ, s.sendQ, pct, truncStr(s.local, 28), proc, marker)
	}

	if len(sockets) > showing {
		fmt.Fprintf(&b, "  ... and %d more sockets (showing top %d by queue usage)\n", len(sockets)-showing, showing)
	}

	if queuedCount > 0 {
		fmt.Fprintf(&b, "\n[*] %d socket(s) have pending connections in queue\n", queuedCount)
	}

	return b.String()
}
