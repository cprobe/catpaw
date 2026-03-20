package sysdiag

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
)

const (
	connLatMaxOutput = 256 * 1024
	connLatMaxGroups = 50
)

func registerConnLatency(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_connlat", "sysdiag:connlat",
		"Connection latency distribution tools (RTT aggregated by remote endpoint). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_connlat", diagnose.DiagnoseTool{
		Name:        "conn_latency_summary",
		Description: "Aggregate TCP RTT by remote IP:port from 'ss -tin'. Shows count, avg, max RTT per remote endpoint, sorted by max RTT descending. Useful for identifying slow downstream services.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "port", Type: "string", Description: "Filter by remote port (optional)"},
			{Name: "top", Type: "string", Description: "Max groups to display (default: 30, max: 50)"},
		},
		Execute: execConnLatency,
	})
}

type connRTT struct {
	remote string
	rttMs  float64
}

type rttGroup struct {
	remote string
	count  int
	sum    float64
	max    float64
	values []float64
}

func (g *rttGroup) avg() float64 {
	if g.count == 0 {
		return 0
	}
	return g.sum / float64(g.count)
}

func (g *rttGroup) p99() float64 {
	if len(g.values) == 0 {
		return 0
	}
	sort.Float64s(g.values)
	idx := int(math.Ceil(float64(len(g.values))*0.99)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(g.values) {
		idx = len(g.values) - 1
	}
	return g.values[idx]
}

func execConnLatency(ctx context.Context, args map[string]string) (string, error) {
	port := strings.TrimSpace(args["port"])
	if port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return "", fmt.Errorf("invalid port: %q (must be 1-65535)", port)
		}
	}

	top := 30
	if v := strings.TrimSpace(args["top"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > connLatMaxGroups {
			return "", fmt.Errorf("top must be 1-%d", connLatMaxGroups)
		}
		top = n
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("conn_latency_summary requires linux (current: %s)", runtime.GOOS)
	}

	raw, err := runSSForRTT(ctx, port)
	if err != nil {
		return "", err
	}

	conns := parseSSRTT(raw)
	groups := groupByRemote(conns)
	return formatConnLatency(groups, top), nil
}

func runSSForRTT(ctx context.Context, port string) (string, error) {
	ss, err := exec.LookPath("ss")
	if err != nil {
		return "", fmt.Errorf("ss not found: %w", err)
	}

	cmdArgs := []string{"-tin", "state", "established"}
	if port != "" {
		cmdArgs = append(cmdArgs, "(", "dport", "=", ":"+port, ")")
	}

	outBuf := &cappedBuf{buf: bytes.NewBuffer(make([]byte, 0, 8192)), max: connLatMaxOutput}
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, ss, cmdArgs...)
	cmd.Stdout = outBuf
	cmd.Stderr = &errBuf

	if err, _ := cmdx.RunTimeout(cmd, ssTimeout); err != nil {
		return "", fmt.Errorf("ss -tin: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return outBuf.buf.String(), nil
}

func parseSSRTT(raw string) []connRTT {
	var conns []connRTT
	lines := strings.Split(raw, "\n")

	var currentRemote string
	for _, line := range lines {
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			fields := strings.Fields(line)
			// ss -tin output: State Recv-Q Send-Q Local Peer
			// or without state: Recv-Q Send-Q Local Peer
			if len(fields) >= 5 {
				currentRemote = fields[4]
			} else if len(fields) >= 4 {
				currentRemote = fields[3]
			} else {
				currentRemote = ""
			}
			continue
		}

		if currentRemote == "" {
			continue
		}

		rttMs := extractRTTFromInfo(strings.TrimSpace(line))
		if rttMs > 0 {
			conns = append(conns, connRTT{remote: stripPort(currentRemote), rttMs: rttMs})
		}
	}
	return conns
}

func extractRTTFromInfo(info string) float64 {
	for _, part := range strings.Fields(info) {
		if strings.HasPrefix(part, "rtt:") {
			rttStr := part[4:]
			slash := strings.IndexByte(rttStr, '/')
			if slash > 0 {
				rttStr = rttStr[:slash]
			}
			v, err := strconv.ParseFloat(rttStr, 64)
			if err == nil {
				return v
			}
		}
	}
	return 0
}

func stripPort(addr string) string {
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		port := addr[idx+1:]
		ip := addr[:idx]
		if _, err := strconv.Atoi(port); err == nil {
			return ip + ":" + port
		}
	}
	return addr
}

const maxValuesPerGroup = 1000

func groupByRemote(conns []connRTT) []rttGroup {
	m := make(map[string]*rttGroup)
	for _, c := range conns {
		g, ok := m[c.remote]
		if !ok {
			g = &rttGroup{remote: c.remote}
			m[c.remote] = g
		}
		g.count++
		g.sum += c.rttMs
		if c.rttMs > g.max {
			g.max = c.rttMs
		}
		if len(g.values) < maxValuesPerGroup {
			g.values = append(g.values, c.rttMs)
		}
	}

	groups := make([]rttGroup, 0, len(m))
	for _, g := range m {
		groups = append(groups, *g)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].max > groups[j].max
	})
	return groups
}

func formatConnLatency(groups []rttGroup, top int) string {
	var b strings.Builder
	b.WriteString("Connection Latency by Remote Endpoint\n")
	b.WriteString(strings.Repeat("=", 55))
	b.WriteString("\n\n")

	if len(groups) == 0 {
		b.WriteString("No established TCP connections with RTT data found.\n")
		return b.String()
	}

	show := len(groups)
	if show > top {
		show = top
	}

	fmt.Fprintf(&b, "%-28s %5s %8s %8s %8s %s\n", "REMOTE", "COUNT", "AVG(ms)", "P99(ms)", "MAX(ms)", "FLAGS")
	b.WriteString(strings.Repeat("-", 75))
	b.WriteString("\n")

	highLat := 0
	for i := 0; i < show; i++ {
		g := groups[i]
		flags := ""
		if g.max >= 500 {
			flags = " [!!!]"
			highLat++
		} else if g.max >= 100 {
			flags = " [!]"
			highLat++
		}

		fmt.Fprintf(&b, "%-28s %5d %8.1f %8.1f %8.1f%s\n",
			truncStr(g.remote, 28), g.count, g.avg(), g.p99(), g.max, flags)
	}

	if len(groups) > show {
		fmt.Fprintf(&b, "  ... and %d more endpoints\n", len(groups)-show)
	}

	if highLat > 0 {
		fmt.Fprintf(&b, "\n[!] %d endpoint(s) with elevated latency (RTT >= 100ms)\n", highLat)
	}

	return b.String()
}
