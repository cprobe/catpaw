package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
)

const (
	retransSnmpPath    = "/proc/net/snmp"
	retransNetstatPath = "/proc/net/netstat"
	retransMaxRead     = 64 * 1024
	retransSampleDelay = time.Second
)

var retransCounters = []string{
	"RetransSegs", "InErrs", "OutRsts", "AttemptFails",
	"EstabResets", "InSegs", "OutSegs", "ActiveOpens", "PassiveOpens",
}

var retransExtCounters = []string{
	"TCPTimeouts", "TCPSpuriousRTOs", "TCPLossProbes",
	"TCPRetransFail", "TCPSynRetrans",
}

func registerRetrans(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_retrans", "sysdiag:retrans",
		"TCP retransmission rate tools (two-sample delta for real-time rates). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_retrans", diagnose.DiagnoseTool{
		Name:        "tcp_retrans_rate",
		Description: "Sample TCP counters twice (1s apart) and compute per-second rates: RetransSegs/s, InErrs/s, OutRsts/s, TCPTimeouts/s, etc. Shows both rates and cumulative totals.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "delay_ms", Type: "string", Description: "Sampling delay in milliseconds (default: 1000, range: 500-5000)"},
		},
		Execute: execRetransRate,
	})
}

func execRetransRate(ctx context.Context, args map[string]string) (string, error) {
	delay := retransSampleDelay
	if v := strings.TrimSpace(args["delay_ms"]); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 500 || ms > 5000 {
			return "", fmt.Errorf("delay_ms must be 500-5000")
		}
		delay = time.Duration(ms) * time.Millisecond
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("tcp_retrans_rate requires linux (current: %s)", runtime.GOOS)
	}

	snap1, err := readTCPCounters()
	if err != nil {
		return "", err
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
	}

	snap2, err := readTCPCounters()
	if err != nil {
		return "", err
	}

	return formatRetransRate(snap1, snap2, delay), nil
}

func readTCPCounters() (map[string]uint64, error) {
	counters := make(map[string]uint64)

	snmp, err := readProcPairFile(retransSnmpPath, "Tcp")
	if err != nil {
		return nil, err
	}
	for k, v := range snmp {
		counters[k] = v
	}

	ext, _ := readProcPairFile(retransNetstatPath, "TcpExt")
	for k, v := range ext {
		counters[k] = v
	}

	return counters, nil
}

// readProcPairFile reads a /proc file with paired header+value lines and extracts one section.
func readProcPairFile(path, section string) (map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, retransMaxRead))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	prefix := section + ":"
	lines := strings.Split(string(data), "\n")
	for i := 0; i+1 < len(lines); i += 2 {
		if !strings.HasPrefix(lines[i], prefix) || !strings.HasPrefix(lines[i+1], prefix) {
			continue
		}

		headers := strings.Fields(lines[i])
		values := strings.Fields(lines[i+1])
		if len(headers) != len(values) {
			continue
		}

		result := make(map[string]uint64, len(headers))
		for j := 1; j < len(headers); j++ {
			v, err := strconv.ParseUint(values[j], 10, 64)
			if err != nil {
				continue
			}
			result[headers[j]] = v
		}
		return result, nil
	}
	return nil, fmt.Errorf("section %q not found in %s", section, path)
}

func formatRetransRate(snap1, snap2 map[string]uint64, delay time.Duration) string {
	secs := delay.Seconds()
	if secs <= 0 {
		secs = 1
	}

	var b strings.Builder
	fmt.Fprintf(&b, "TCP Retransmission & Error Rates (sampled over %.1fs)\n", secs)
	b.WriteString(strings.Repeat("=", 55))
	b.WriteString("\n\n")

	b.WriteString("Core TCP rates (/proc/net/snmp):\n")
	hasIssue := false
	for _, key := range retransCounters {
		v1, ok1 := snap1[key]
		v2, ok2 := snap2[key]
		if !ok1 || !ok2 {
			continue
		}
		delta := safeDelta(v2, v1)
		rate := float64(delta) / secs

		marker := ""
		if isErrorCounter(key) && rate > 0 {
			marker = " [!]"
			hasIssue = true
		}

		fmt.Fprintf(&b, "  %-18s %10d  (%8.1f/s)  total=%d%s\n",
			key, delta, rate, v2, marker)
	}

	b.WriteString("\nExtended TCP counters (/proc/net/netstat TcpExt):\n")
	for _, key := range retransExtCounters {
		v1, ok1 := snap1[key]
		v2, ok2 := snap2[key]
		if !ok1 || !ok2 {
			continue
		}
		delta := safeDelta(v2, v1)
		rate := float64(delta) / secs

		marker := ""
		if rate > 0 {
			marker = " [!]"
			hasIssue = true
		}

		fmt.Fprintf(&b, "  %-18s %10d  (%8.1f/s)  total=%d%s\n",
			key, delta, rate, v2, marker)
	}

	if hasIssue {
		b.WriteString("\n[!] Non-zero error/retransmission rates detected during sampling.\n")
	}

	if outSegs1, ok1 := snap1["OutSegs"]; ok1 {
		if outSegs2, ok2 := snap2["OutSegs"]; ok2 {
			if retrans1, ok3 := snap1["RetransSegs"]; ok3 {
				if retrans2, ok4 := snap2["RetransSegs"]; ok4 {
					outDelta := safeDelta(outSegs2, outSegs1)
					retDelta := safeDelta(retrans2, retrans1)
					if outDelta > 0 {
						pct := float64(retDelta) / float64(outDelta) * 100
						marker := ""
						if pct >= 5 {
							marker = " [!!!]"
						} else if pct >= 1 {
							marker = " [!]"
						}
						fmt.Fprintf(&b, "\nRetransmission ratio: %.2f%% (%d retrans / %d sent)%s\n",
							pct, retDelta, outDelta, marker)
					}
				}
			}
		}
	}

	return b.String()
}

func safeDelta(v2, v1 uint64) uint64 {
	if v2 >= v1 {
		return v2 - v1
	}
	return 0
}

func isErrorCounter(key string) bool {
	switch key {
	case "RetransSegs", "InErrs", "OutRsts", "AttemptFails", "EstabResets":
		return true
	}
	return false
}
