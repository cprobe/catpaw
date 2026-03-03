package sockstat

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
)

var _ plugins.Diagnosable = (*SockstatPlugin)(nil)

var interestingKeys = map[string]bool{
	"ListenOverflows":   true,
	"ListenDrops":       true,
	"TCPAbortOnTimeout": true,
	"TCPAbortOnData":    true,
	"TCPAbortOnClose":   true,
	"TCPAbortOnMemory":  true,
	"TCPAbortOnLinger":  true,
	"TCPTimeouts":       true,
	"TCPRetransFail":    true,
	"TCPSynRetrans":     true,
	"TCPOFOQueue":       true,
	"TCPOFODrop":        true,
	"TCPBacklogDrop":    true,
	"PFMemallocDrop":    true,
	"TCPRcvQDrop":       true,
}

func (p *SockstatPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sockstat", "sockstat",
		"Socket and network statistics (TcpExt, TCP, UDP counters). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sockstat", diagnose.DiagnoseTool{
		Name:        "sockstat_summary",
		Description: "Show key TcpExt counters from /proc/net/netstat (overflows, drops, aborts, retransmits, timeouts)",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execSockstatSummary,
	})

	registerNetstatSummary(registry)
}

func execSockstatSummary(_ context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("sockstat_summary requires linux (current: %s)", runtime.GOOS)
	}

	counters, err := readTcpExtCounters()
	if err != nil {
		return "", err
	}

	keys := make([]string, 0, len(counters))
	maxKeyLen := 0
	for k := range counters {
		keys = append(keys, k)
		if len(k) > maxKeyLen {
			maxKeyLen = len(k)
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("TcpExt counters (cumulative since boot):\n\n")
	fmtStr := fmt.Sprintf("  %%-%ds  %%d\n", maxKeyLen)
	for _, k := range keys {
		fmt.Fprintf(&b, fmtStr, k, counters[k])
	}
	return b.String(), nil
}

func readTcpExtCounters() (map[string]uint64, error) {
	data, err := os.ReadFile(netstatPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", netstatPath, err)
	}

	lines := strings.Split(string(data), "\n")

	var headerLine, valueLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "TcpExt:") {
			if headerLine == "" {
				headerLine = line
			} else {
				valueLine = line
				break
			}
		}
	}

	if headerLine == "" || valueLine == "" {
		return nil, fmt.Errorf("TcpExt section not found in %s", netstatPath)
	}

	headers := strings.Fields(headerLine)
	values := strings.Fields(valueLine)
	if len(headers) != len(values) {
		return nil, fmt.Errorf("TcpExt header/value count mismatch (%d vs %d)", len(headers), len(values))
	}

	result := make(map[string]uint64)
	for i := 1; i < len(headers); i++ {
		if !interestingKeys[headers[i]] {
			continue
		}
		v, err := strconv.ParseUint(values[i], 10, 64)
		if err != nil {
			continue
		}
		result[headers[i]] = v
	}
	return result, nil
}
