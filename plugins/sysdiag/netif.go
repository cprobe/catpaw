package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cprobe/digcore/diagnose"
)

const (
	netDevPath    = "/proc/net/dev"
	netDevMaxSize = 64 * 1024
)

func registerNetInterface(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_netif", "sysdiag:netif",
		"Network interface diagnostic tools (RX/TX bytes, packets, drops, errors). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_netif", diagnose.DiagnoseTool{
		Name:        "net_interface",
		Description: "Show per-interface network statistics: RX/TX bytes, packets, errors, drops. Sorted by total traffic. Highlights interfaces with non-zero errors or drops.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "show_lo", Type: "string", Description: "Set to 'true' to include loopback interface (default: false)"},
		},
		Execute: execNetInterface,
	})
}

type netIfEntry struct {
	name      string
	rxBytes   uint64
	rxPackets uint64
	rxErrors  uint64
	rxDrops   uint64
	txBytes   uint64
	txPackets uint64
	txErrors  uint64
	txDrops   uint64
}

func (e *netIfEntry) totalBytes() uint64 { return e.rxBytes + e.txBytes }
func (e *netIfEntry) hasProblems() bool  { return e.rxErrors+e.rxDrops+e.txErrors+e.txDrops > 0 }

func execNetInterface(_ context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("net_interface requires linux (current: %s)", runtime.GOOS)
	}

	showLo := strings.ToLower(args["show_lo"]) == "true"

	entries, err := parseNetDev(netDevPath)
	if err != nil {
		return "", err
	}

	if !showLo {
		filtered := make([]netIfEntry, 0, len(entries))
		for _, e := range entries {
			if e.name != "lo" {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].totalBytes() > entries[j].totalBytes()
	})

	return formatNetIf(entries), nil
}

func parseNetDev(path string) ([]netIfEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, netDevMaxSize))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	// First two lines are headers
	if len(lines) < 3 {
		return nil, nil
	}

	var entries []netIfEntry
	for _, line := range lines[2:] {
		e, ok := parseNetDevLine(line)
		if ok {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// parseNetDevLine parses a line from /proc/net/dev.
// Format: "  iface: rx_bytes rx_packets rx_errs rx_drop rx_fifo rx_frame rx_compressed rx_multicast tx_bytes tx_packets tx_errs tx_drop tx_fifo tx_colls tx_carrier tx_compressed"
func parseNetDevLine(line string) (netIfEntry, bool) {
	colonIdx := strings.IndexByte(line, ':')
	if colonIdx < 0 {
		return netIfEntry{}, false
	}

	name := strings.TrimSpace(line[:colonIdx])
	if name == "" {
		return netIfEntry{}, false
	}

	fields := strings.Fields(line[colonIdx+1:])
	if len(fields) < 16 {
		return netIfEntry{}, false
	}

	return netIfEntry{
		name:      name,
		rxBytes:   parseU64(fields[0]),
		rxPackets: parseU64(fields[1]),
		rxErrors:  parseU64(fields[2]),
		rxDrops:   parseU64(fields[3]),
		txBytes:   parseU64(fields[8]),
		txPackets: parseU64(fields[9]),
		txErrors:  parseU64(fields[10]),
		txDrops:   parseU64(fields[11]),
	}, true
}

func parseU64(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func formatNetIf(entries []netIfEntry) string {
	if len(entries) == 0 {
		return "No network interfaces found."
	}

	problemCount := 0
	for _, e := range entries {
		if e.hasProblems() {
			problemCount++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Network Interfaces: %d", len(entries))
	if problemCount > 0 {
		fmt.Fprintf(&b, " [!] %d with errors/drops", problemCount)
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "%-16s  %10s  %10s  %10s  %10s  %8s  %8s  %8s  %8s\n",
		"INTERFACE", "RX_BYTES", "RX_PKTS", "TX_BYTES", "TX_PKTS", "RX_ERR", "RX_DROP", "TX_ERR", "TX_DROP")
	b.WriteString(strings.Repeat("-", 110))
	b.WriteByte('\n')

	for _, e := range entries {
		marker := ""
		if e.hasProblems() {
			marker = " [!]"
		}
		fmt.Fprintf(&b, "%-16s  %10s  %10s  %10s  %10s  %8d  %8d  %8d  %8d%s\n",
			e.name,
			humanBytes(e.rxBytes),
			humanPkts(e.rxPackets),
			humanBytes(e.txBytes),
			humanPkts(e.txPackets),
			e.rxErrors, e.rxDrops,
			e.txErrors, e.txDrops,
			marker)
	}
	return b.String()
}

func humanPkts(n uint64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fG", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return strconv.FormatUint(n, 10)
	}
}
