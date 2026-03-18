package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/cprobe/digcore/diagnose"
)

const (
	ctCountPath = "/proc/sys/net/netfilter/nf_conntrack_count"
	ctMaxPath   = "/proc/sys/net/netfilter/nf_conntrack_max"
	ctStatPath  = "/proc/net/stat/nf_conntrack"
	ctMaxRead   = 64 * 1024
)

func registerConntrackStat(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_ct", "sysdiag:conntrack",
		"Connection tracking diagnostic tools (conntrack usage, drops, inserts). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_ct", diagnose.DiagnoseTool{
		Name:        "conntrack_stat",
		Description: "Show connection tracking table usage (count/max) and kernel statistics (drops, insert failures, early drops, search restarts). Requires nf_conntrack module.",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execConntrackStat,
	})
}

type ctStat struct {
	entries       uint64
	searched      uint64
	found         uint64
	new           uint64
	invalid       uint64
	ignore        uint64
	delete        uint64
	deleteList    uint64
	insert        uint64
	insertFailed  uint64
	drop          uint64
	earlyDrop     uint64
	searchRestart uint64
}

func execConntrackStat(_ context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("conntrack_stat requires linux (current: %s)", runtime.GOOS)
	}

	count, cErr := readUint64File(ctCountPath)
	max, mErr := readUint64File(ctMaxPath)

	stats, sErr := parseConntrackStat(ctStatPath)

	if cErr != nil && mErr != nil && sErr != nil {
		return "", fmt.Errorf("nf_conntrack module not loaded (cannot read count, max, or stats)")
	}

	return formatConntrackStat(count, max, cErr, mErr, stats, sErr), nil
}

func readUint64File(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

// parseConntrackStat reads /proc/net/stat/nf_conntrack and sums across all CPUs.
// Format: header line with field names, then one line per CPU with hex values.
func parseConntrackStat(path string) (ctStat, error) {
	f, err := os.Open(path)
	if err != nil {
		return ctStat{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, ctMaxRead))
	if err != nil {
		return ctStat{}, fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return ctStat{}, fmt.Errorf("unexpected format in %s", path)
	}

	headers := strings.Fields(lines[0])
	headerIdx := make(map[string]int, len(headers))
	for i, h := range headers {
		headerIdx[h] = i
	}

	var sum ctStat
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		sum.entries += hexField(fields, headerIdx, "entries")
		sum.searched += hexField(fields, headerIdx, "searched")
		sum.found += hexField(fields, headerIdx, "found")
		sum.new += hexField(fields, headerIdx, "new")
		sum.invalid += hexField(fields, headerIdx, "invalid")
		sum.ignore += hexField(fields, headerIdx, "ignore")
		sum.delete += hexField(fields, headerIdx, "delete")
		sum.deleteList += hexField(fields, headerIdx, "delete_list")
		sum.insert += hexField(fields, headerIdx, "insert")
		sum.insertFailed += hexField(fields, headerIdx, "insert_failed")
		sum.drop += hexField(fields, headerIdx, "drop")
		sum.earlyDrop += hexField(fields, headerIdx, "early_drop")
		sum.searchRestart += hexField(fields, headerIdx, "search_restart")
	}
	return sum, nil
}

func hexField(fields []string, idx map[string]int, name string) uint64 {
	i, ok := idx[name]
	if !ok || i >= len(fields) {
		return 0
	}
	v, _ := strconv.ParseUint(fields[i], 16, 64)
	return v
}

func formatConntrackStat(count, max uint64, cErr, mErr error, stats ctStat, sErr error) string {
	var b strings.Builder
	b.WriteString("Connection Tracking (nf_conntrack)\n\n")

	if cErr == nil && mErr == nil {
		pct := float64(0)
		if max > 0 {
			pct = float64(count) / float64(max) * 100
		}
		marker := ""
		if pct >= 90 {
			marker = " [!!!]"
		} else if pct >= 75 {
			marker = " [!]"
		}
		fmt.Fprintf(&b, "Usage:  %d / %d  (%.1f%%)%s\n", count, max, pct, marker)
	} else {
		if cErr != nil {
			fmt.Fprintf(&b, "Count:  (unavailable: %v)\n", cErr)
		}
		if mErr != nil {
			fmt.Fprintf(&b, "Max:    (unavailable: %v)\n", mErr)
		}
	}

	if sErr != nil {
		fmt.Fprintf(&b, "\nStatistics: unavailable (%v)\n", sErr)
		return b.String()
	}

	b.WriteString("\nKernel Statistics (cumulative, summed across all CPUs):\n\n")

	type statRow struct {
		label string
		value uint64
		note  string
	}
	rows := []statRow{
		{"Insert", stats.insert, ""},
		{"Insert Failed", stats.insertFailed, "table full or clash"},
		{"Drop", stats.drop, "packet dropped due to full table"},
		{"Early Drop", stats.earlyDrop, "dropped non-assured entry to make room"},
		{"Invalid", stats.invalid, "packets failing validation"},
		{"Search Restart", stats.searchRestart, "hash resize/race restarts"},
		{"New", stats.new, "new connections created"},
		{"Delete", stats.delete, "connections removed"},
		{"Searched", stats.searched, "hash table lookups"},
		{"Found", stats.found, "successful lookups"},
	}

	for _, r := range rows {
		marker := ""
		if (r.label == "Drop" || r.label == "Insert Failed" || r.label == "Early Drop") && r.value > 0 {
			marker = " [!]"
		}
		fmt.Fprintf(&b, "  %-18s %12d%s", r.label, r.value, marker)
		if r.note != "" {
			fmt.Fprintf(&b, "  (%s)", r.note)
		}
		b.WriteByte('\n')
	}

	return b.String()
}
