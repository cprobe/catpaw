package filefd

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

const (
	maxTopProcs    = 100
	maxProcsToScan = 2000
)

var _ plugins.Diagnosable = (*FilefdPlugin)(nil)

func (p *FilefdPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("filefd", "filefd",
		"File descriptor diagnostic tools (system-wide usage, top processes by fd count). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("filefd", diagnose.DiagnoseTool{
		Name:        "filefd_usage",
		Description: "Show system-wide file descriptor usage (allocated, max, usage percent) from /proc/sys/fs/file-nr",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execFilefdUsage,
	})

	registry.Register("filefd", diagnose.DiagnoseTool{
		Name:        "filefd_top_procs",
		Description: "Show top processes by open file descriptor count (default top 10, max 100). Linux only.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "count", Type: "string", Description: "Number of top processes to show (default 10, max 100)"},
		},
		Execute: execFilefdTopProcs,
	})
}

func execFilefdUsage(_ context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("filefd_usage requires linux (current: %s)", runtime.GOOS)
	}

	allocated, max, err := readFileNr()
	if err != nil {
		return "", err
	}

	var pct float64
	if max > 0 {
		pct = float64(allocated) / float64(max) * 100
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Allocated: %d\n", allocated)
	fmt.Fprintf(&b, "Max:       %d\n", max)
	fmt.Fprintf(&b, "Usage:     %.1f%%\n", pct)
	return b.String(), nil
}

func execFilefdTopProcs(ctx context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("filefd_top_procs requires linux (current: %s)", runtime.GOOS)
	}

	count := 10
	if s := args["count"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid count %q: must be a positive integer", s)
		}
		count = n
	}
	if count > maxTopProcs {
		count = maxTopProcs
	}

	procs, err := listProcFdCounts(ctx)
	if err != nil {
		return "", err
	}

	sort.Slice(procs, func(i, j int) bool { return procs[i].fds > procs[j].fds })

	if count > len(procs) {
		count = len(procs)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Total processes scanned: %d\n\n", len(procs))
	fmt.Fprintf(&b, "%7s  %6s  %s\n", "PID", "FDs", "Comm")
	for i := 0; i < count; i++ {
		fmt.Fprintf(&b, "%7d  %6d  %s\n", procs[i].pid, procs[i].fds, procs[i].comm)
	}
	return b.String(), nil
}

type procFdInfo struct {
	pid  int
	fds  int
	comm string
}

func listProcFdCounts(ctx context.Context) ([]procFdInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}

	result := make([]procFdInfo, 0, min(len(entries)/2, maxProcsToScan))
	for _, e := range entries {
		if len(result) >= maxProcsToScan {
			break
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}

		fds := countFds(pid)
		if fds < 0 {
			continue
		}

		comm := sanitizeComm(readComm(pid))
		result = append(result, procFdInfo{pid: pid, fds: fds, comm: comm})
	}
	return result, nil
}

func countFds(pid int) int {
	d, err := os.Open(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return -1
	}
	defer d.Close()

	names, err := d.Readdirnames(-1)
	if err != nil {
		return -1
	}
	return len(names)
}

func readComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(data))
}

func sanitizeComm(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

