package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/diagnose"
)

const sysctlMaxRead = 512

func registerTCPTune(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_tcptune", "sysdiag:tcptune",
		"TCP tuning check tools (kernel parameter snapshot with recommendations). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_tcptune", diagnose.DiagnoseTool{
		Name:        "tcp_tuning_check",
		Description: "Show all timeout-related TCP kernel parameters with current values and recommended ranges. Highlights parameters outside recommended range. Covers SYN retries, keepalive, backlog, memory, congestion, and TIME_WAIT settings.",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execTCPTuneCheck,
	})
}

type tcpParam struct {
	sysctl      string
	description string
	recMin      int64
	recMax      int64
	category    string
}

var tcpParams = []tcpParam{
	// Timeout / Retry
	{"net.ipv4.tcp_syn_retries", "SYN retransmissions before giving up", 2, 6, "Timeout/Retry"},
	{"net.ipv4.tcp_synack_retries", "SYN-ACK retransmissions for passive connections", 2, 5, "Timeout/Retry"},
	{"net.ipv4.tcp_retries1", "Retransmissions before IP layer notified", 3, 3, "Timeout/Retry"},
	{"net.ipv4.tcp_retries2", "Retransmissions before dropping connection", 8, 15, "Timeout/Retry"},
	{"net.ipv4.tcp_orphan_retries", "Retransmissions for orphaned sockets", 0, 8, "Timeout/Retry"},
	{"net.ipv4.tcp_fin_timeout", "FIN-WAIT-2 timeout (seconds)", 15, 60, "Timeout/Retry"},

	// Keepalive
	{"net.ipv4.tcp_keepalive_time", "Keepalive idle time (seconds)", 60, 7200, "Keepalive"},
	{"net.ipv4.tcp_keepalive_intvl", "Keepalive probe interval (seconds)", 10, 75, "Keepalive"},
	{"net.ipv4.tcp_keepalive_probes", "Keepalive probes before drop", 3, 9, "Keepalive"},

	// Backlog / Queue
	{"net.core.somaxconn", "Max listen backlog", 128, -1, "Backlog"},
	{"net.ipv4.tcp_max_syn_backlog", "Max SYN queue length", 128, -1, "Backlog"},
	{"net.core.netdev_max_backlog", "Max per-CPU input queue", 1000, -1, "Backlog"},

	// TIME_WAIT
	{"net.ipv4.tcp_tw_reuse", "Reuse TIME_WAIT sockets (1=enabled)", 0, 2, "TIME_WAIT"},
	{"net.ipv4.tcp_max_tw_buckets", "Max TIME_WAIT sockets", 1000, -1, "TIME_WAIT"},

	// Memory
	{"net.ipv4.tcp_rmem", "TCP receive buffer (min default max)", 0, -1, "Memory"},
	{"net.ipv4.tcp_wmem", "TCP send buffer (min default max)", 0, -1, "Memory"},
	{"net.core.rmem_max", "Max receive buffer size", 0, -1, "Memory"},
	{"net.core.wmem_max", "Max send buffer size", 0, -1, "Memory"},

	// Congestion
	{"net.ipv4.tcp_congestion_control", "Congestion control algorithm", 0, -1, "Congestion"},
	{"net.ipv4.tcp_slow_start_after_idle", "Slow start after idle (0=disabled)", 0, 1, "Congestion"},
}

func execTCPTuneCheck(_ context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("tcp_tuning_check requires linux (current: %s)", runtime.GOOS)
	}

	return formatTCPTune(readAllTCPParams()), nil
}

type paramResult struct {
	param tcpParam
	value string
	note  string
}

func readAllTCPParams() []paramResult {
	var results []paramResult
	for _, p := range tcpParams {
		path := "/proc/sys/" + strings.ReplaceAll(p.sysctl, ".", "/")
		val := readSysctlValue(path)
		note := evaluateParam(p, val)
		results = append(results, paramResult{param: p, value: val, note: note})
	}
	return results
}

func readSysctlValue(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "(not available)"
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, sysctlMaxRead))
	if err != nil {
		return "(read error)"
	}
	return strings.TrimSpace(string(data))
}

func evaluateParam(p tcpParam, value string) string {
	if value == "(not available)" || value == "(read error)" {
		return ""
	}

	if p.sysctl == "net.ipv4.tcp_congestion_control" {
		return ""
	}

	if strings.Contains(value, "\t") || strings.Contains(value, " ") {
		return ""
	}

	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return ""
	}

	if p.recMax == -1 {
		if p.recMin > 0 && v < p.recMin {
			return fmt.Sprintf("[!] below recommended minimum (%d)", p.recMin)
		}
		return ""
	}

	if v < p.recMin {
		return fmt.Sprintf("[!] below range [%d, %d]", p.recMin, p.recMax)
	}
	if v > p.recMax {
		return fmt.Sprintf("[!] above range [%d, %d]", p.recMin, p.recMax)
	}
	return ""
}

func formatTCPTune(results []paramResult) string {
	var b strings.Builder
	b.WriteString("TCP Tuning Parameters Check\n")
	b.WriteString(strings.Repeat("=", 55))
	b.WriteString("\n\n")

	currentCat := ""
	issueCount := 0

	for _, r := range results {
		if r.param.category != currentCat {
			if currentCat != "" {
				b.WriteString("\n")
			}
			currentCat = r.param.category
			fmt.Fprintf(&b, "[%s]\n", currentCat)
		}

		note := ""
		if r.note != "" {
			note = "  " + r.note
			issueCount++
		}

		fmt.Fprintf(&b, "  %-40s = %-20s%s\n", r.param.sysctl, r.value, note)
	}

	b.WriteString("\n" + strings.Repeat("-", 55) + "\n")
	if issueCount > 0 {
		fmt.Fprintf(&b, "[!] %d parameter(s) outside recommended range.\n", issueCount)
	} else {
		b.WriteString("All parameters within recommended ranges.\n")
	}

	return b.String()
}
