package sysdiag

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/diagnose"
)

const (
	defaultIOTopCount = 10
	maxIOTopCount     = 100
	maxIOProcsToScan  = 2000
)

func registerIOTop(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_io", "sysdiag:io",
		"I/O diagnostic tools (per-process I/O statistics from /proc). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_io", diagnose.DiagnoseTool{
		Name:        "io_top",
		Description: "Show top processes by I/O bytes (read_bytes + write_bytes from /proc/<pid>/io). Requires root or CAP_SYS_PTRACE for other users' processes.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "count", Type: "string", Description: "Number of top processes to show (default 10, max 100)"},
			{Name: "sort_by", Type: "string", Description: "Sort field: total (default), read, write"},
		},
		Execute: execIOTop,
	})
}

type procIOInfo struct {
	pid        int
	comm       string
	readBytes  uint64
	writeBytes uint64
}

func (p *procIOInfo) total() uint64 {
	return p.readBytes + p.writeBytes
}

func execIOTop(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("io_top requires linux (current: %s)", runtime.GOOS)
	}

	count := defaultIOTopCount
	if s := args["count"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid count %q: must be a positive integer", s)
		}
		if n > maxIOTopCount {
			n = maxIOTopCount
		}
		count = n
	}

	sortBy := "total"
	if s := args["sort_by"]; s != "" {
		switch strings.ToLower(s) {
		case "total", "read", "write":
			sortBy = strings.ToLower(s)
		default:
			return "", fmt.Errorf("invalid sort_by %q: must be total, read, or write", s)
		}
	}

	procs, skipped, err := collectProcIO(ctx)
	if err != nil {
		return "", err
	}

	sortProcs(procs, sortBy)

	if count > len(procs) {
		count = len(procs)
	}

	return formatIOTop(procs[:count], len(procs), skipped, sortBy), nil
}

func collectProcIO(ctx context.Context) ([]procIOInfo, int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, 0, fmt.Errorf("read /proc: %w", err)
	}

	result := make([]procIOInfo, 0, min(len(entries)/2, maxIOProcsToScan))
	skipped := 0

	for _, e := range entries {
		if len(result)+skipped >= maxIOProcsToScan {
			break
		}
		select {
		case <-ctx.Done():
			return result, skipped, ctx.Err()
		default:
		}
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}

		info, ok := readProcIO(pid)
		if !ok {
			skipped++
			continue
		}
		result = append(result, info)
	}
	return result, skipped, nil
}

// readProcIO reads /proc/<pid>/io and extracts read_bytes and write_bytes.
// These are the actual storage I/O bytes (bypassing page cache where tracked).
func readProcIO(pid int) (procIOInfo, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/io", pid))
	if err != nil {
		return procIOInfo{}, false
	}

	info := procIOInfo{pid: pid}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := parseIOLine(line)
		if !ok {
			continue
		}
		switch k {
		case "read_bytes":
			info.readBytes = v
		case "write_bytes":
			info.writeBytes = v
		}
	}

	commData, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		info.comm = "?"
	} else {
		info.comm = sanitizeIOComm(strings.TrimSpace(string(commData)))
	}

	return info, true
}

func parseIOLine(line string) (string, uint64, bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", 0, false
	}
	key := strings.TrimSpace(line[:idx])
	val := strings.TrimSpace(line[idx+1:])
	if val == "" {
		return key, 0, false
	}
	n, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return key, 0, false
	}
	return key, n, true
}

func sanitizeIOComm(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

func sortProcs(procs []procIOInfo, sortBy string) {
	sort.Slice(procs, func(i, j int) bool {
		switch sortBy {
		case "read":
			return procs[i].readBytes > procs[j].readBytes
		case "write":
			return procs[i].writeBytes > procs[j].writeBytes
		default:
			return procs[i].total() > procs[j].total()
		}
	})
}

func formatIOTop(procs []procIOInfo, totalScanned, skipped int, sortBy string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Processes scanned: %d  (skipped %d - permission denied or exited)\n", totalScanned, skipped)
	fmt.Fprintf(&b, "Sorted by: %s\n\n", sortBy)

	fmt.Fprintf(&b, "%7s  %-20s  %12s  %12s  %12s\n",
		"PID", "PROCESS", "READ", "WRITE", "TOTAL")
	fmt.Fprintf(&b, "%7s  %-20s  %12s  %12s  %12s\n",
		"-------", strings.Repeat("-", 20),
		"------------", "------------", "------------")

	for _, p := range procs {
		fmt.Fprintf(&b, "%7d  %-20s  %12s  %12s  %12s\n",
			p.pid,
			truncName(p.comm, 20),
			humanBytes(p.readBytes),
			humanBytes(p.writeBytes),
			humanBytes(p.total()),
		)
	}
	return b.String()
}

func humanBytes(b uint64) string {
	const (
		kiB = uint64(1) << 10
		miB = uint64(1) << 20
		giB = uint64(1) << 30
		tiB = uint64(1) << 40
	)
	switch {
	case b >= tiB:
		return fmt.Sprintf("%.1fT", float64(b)/float64(tiB))
	case b >= giB:
		return fmt.Sprintf("%.1fG", float64(b)/float64(giB))
	case b >= miB:
		return fmt.Sprintf("%.1fM", float64(b)/float64(miB))
	case b >= kiB:
		return fmt.Sprintf("%.1fK", float64(b)/float64(kiB))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
