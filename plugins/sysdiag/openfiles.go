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
	defaultOpenFilesMax = 100
	maxOpenFilesMax     = 500
	maxFDsToScan       = 10000
)

func registerOpenFiles(registry *diagnose.ToolRegistry) {
	registry.Register("sysdiag_proc", diagnose.DiagnoseTool{
		Name:        "open_files",
		Description: "List open file descriptors of a process with their targets (files, sockets, pipes). Requires root or same UID.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "pid", Type: "string", Description: "Process ID (required)", Required: true},
			{Name: "max_fds", Type: "string", Description: "Max FDs to show (default 100, max 500)"},
		},
		Execute: execOpenFiles,
	})
}

type fdEntry struct {
	fd     int
	target string
	kind   string // file, socket, pipe, anon_inode, other
}

func execOpenFiles(ctx context.Context, args map[string]string) (string, error) {
	pidStr := strings.TrimSpace(args["pid"])
	if pidStr == "" {
		return "", fmt.Errorf("pid parameter is required")
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return "", fmt.Errorf("invalid pid %q: must be a positive integer", pidStr)
	}

	maxFDs := defaultOpenFilesMax
	if s := args["max_fds"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid max_fds %q: must be a positive integer", s)
		}
		if n > maxOpenFilesMax {
			n = maxOpenFilesMax
		}
		maxFDs = n
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("open_files requires linux (current: %s)", runtime.GOOS)
	}

	entries, totalFDs, err := readProcFDs(ctx, pid, maxFDs)
	if err != nil {
		return "", err
	}

	return formatOpenFiles(pid, entries, totalFDs, maxFDs), nil
}

func readProcFDs(ctx context.Context, pid, maxShow int) ([]fdEntry, int, error) {
	dirPath := fmt.Sprintf("/proc/%d/fd", pid)
	dir, err := os.Open(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, fmt.Errorf("process %d not found", pid)
		}
		if os.IsPermission(err) {
			return nil, 0, fmt.Errorf("permission denied reading fds of process %d (requires root or same UID)", pid)
		}
		return nil, 0, fmt.Errorf("open %s: %w", dirPath, err)
	}
	defer dir.Close()

	names, err := dir.Readdirnames(maxFDsToScan)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", dirPath, err)
	}

	totalFDs := len(names)
	// Sort numerically for consistent output
	sort.Slice(names, func(i, j int) bool {
		a, _ := strconv.Atoi(names[i])
		b, _ := strconv.Atoi(names[j])
		return a < b
	})

	limit := maxShow
	if limit > len(names) {
		limit = len(names)
	}

	entries := make([]fdEntry, 0, limit)
	for i := 0; i < limit; i++ {
		select {
		case <-ctx.Done():
			return entries, totalFDs, ctx.Err()
		default:
		}

		fdNum, err := strconv.Atoi(names[i])
		if err != nil {
			continue
		}

		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, fdNum))
		if err != nil {
			target = "(error)"
		}

		entries = append(entries, fdEntry{
			fd:     fdNum,
			target: target,
			kind:   classifyFD(target),
		})
	}
	return entries, totalFDs, nil
}

func classifyFD(target string) string {
	switch {
	case strings.HasPrefix(target, "socket:"):
		return "socket"
	case strings.HasPrefix(target, "pipe:"):
		return "pipe"
	case strings.HasPrefix(target, "anon_inode:"):
		return "anon"
	case strings.HasPrefix(target, "/"):
		return "file"
	case target == "(error)":
		return "?"
	default:
		return "other"
	}
}

func formatOpenFiles(pid int, entries []fdEntry, totalFDs, maxFDs int) string {
	if len(entries) == 0 {
		return fmt.Sprintf("No open file descriptors found for process %d.", pid)
	}

	// Summary by type
	typeCounts := make(map[string]int)
	for _, e := range entries {
		typeCounts[e.kind]++
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Open FDs for PID %d: %d total", pid, totalFDs)
	if totalFDs > maxFDs {
		fmt.Fprintf(&b, " (showing first %d)", maxFDs)
	}
	b.WriteString("\n")

	// Type summary
	b.WriteString("By type: ")
	types := make([]string, 0, len(typeCounts))
	for k := range typeCounts {
		types = append(types, k)
	}
	sort.Strings(types)
	for i, k := range types {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%d", k, typeCounts[k])
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "%5s  %-7s  %s\n", "FD", "TYPE", "TARGET")
	fmt.Fprintf(&b, "%5s  %-7s  %s\n", "-----", "-------", strings.Repeat("-", 50))

	for _, e := range entries {
		target := e.target
		if len(target) > 120 {
			target = target[:117] + "..."
		}
		fmt.Fprintf(&b, "%5d  %-7s  %s\n", e.fd, e.kind, target)
	}
	return b.String()
}
