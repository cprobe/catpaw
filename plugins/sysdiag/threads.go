package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

const (
	threadMaxShow    = 200
	threadDefaultMax = 50
	threadStatMax    = 1024
)

func registerThreads(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_proc", "sysdiag:proc",
		"Process-level diagnostic tools. Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_proc", diagnose.DiagnoseTool{
		Name:        "proc_threads",
		Description: "List threads of a process from /proc/<pid>/task/, showing TID, name, state, CPU time, and wchan (kernel wait function). D-state threads show which kernel function they are blocked on. Sorted by total CPU time descending.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "pid", Type: "string", Description: "Process ID (required)", Required: true},
			{Name: "max_threads", Type: "string", Description: "Maximum threads to display (default: 50, max: 200)"},
		},
		Execute: execProcThreads,
	})
}

type threadInfo struct {
	tid   int
	comm  string
	state byte
	utime uint64
	stime uint64
	wchan string
}

func (t *threadInfo) totalCPU() uint64 { return t.utime + t.stime }

func execProcThreads(ctx context.Context, args map[string]string) (string, error) {
	pidStr := strings.TrimSpace(args["pid"])
	if pidStr == "" {
		return "", fmt.Errorf("pid parameter is required")
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return "", fmt.Errorf("invalid pid: %q", pidStr)
	}

	maxShow := threadDefaultMax
	if v := strings.TrimSpace(args["max_threads"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > threadMaxShow {
			return "", fmt.Errorf("max_threads must be 1-%d", threadMaxShow)
		}
		maxShow = n
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("proc_threads requires linux (current: %s)", runtime.GOOS)
	}

	taskDir := fmt.Sprintf("/proc/%d/task", pid)
	threads, err := readThreads(ctx, taskDir, pid)
	if err != nil {
		return "", err
	}

	return formatThreads(threads, pid, maxShow), nil
}

func readThreads(ctx context.Context, taskDir string, pid int) ([]threadInfo, error) {
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("process %d not found or already exited", pid)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("permission denied reading /proc/%d/task", pid)
		}
		return nil, fmt.Errorf("read %s: %w", taskDir, err)
	}

	var threads []threadInfo
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}

		tid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		ti := threadInfo{tid: tid}
		ti.comm = readThreadComm(taskDir, tid)
		ti.state, ti.utime, ti.stime = readThreadStat(taskDir, tid)
		ti.wchan = readThreadWchan(taskDir, tid)
		threads = append(threads, ti)
	}

	return threads, nil
}

func readThreadComm(taskDir string, tid int) string {
	path := filepath.Join(taskDir, strconv.Itoa(tid), "comm")
	f, err := os.Open(path)
	if err != nil {
		return "?"
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(data), ""))
}

func readThreadWchan(taskDir string, tid int) string {
	path := filepath.Join(taskDir, strconv.Itoa(tid), "wchan")
	f, err := os.Open(path)
	if err != nil {
		if os.IsPermission(err) {
			return "?(perm)"
		}
		return ""
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return "?(read)"
	}
	w := strings.TrimSpace(string(data))
	if w == "0" || w == "" {
		return ""
	}
	return w
}

func readThreadStat(taskDir string, tid int) (byte, uint64, uint64) {
	path := filepath.Join(taskDir, strconv.Itoa(tid), "stat")
	f, err := os.Open(path)
	if err != nil {
		return '?', 0, 0
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, threadStatMax))
	if err != nil {
		return '?', 0, 0
	}

	return parseThreadStat(string(data))
}

// parseThreadStat extracts state(field 3), utime(field 14), stime(field 15) from /proc/pid/stat.
// Fields are space-separated, but field 2 (comm) is in parentheses and may contain spaces.
func parseThreadStat(line string) (byte, uint64, uint64) {
	closeP := strings.LastIndex(line, ")")
	if closeP < 0 || closeP+2 >= len(line) {
		return '?', 0, 0
	}

	rest := line[closeP+2:]
	fields := strings.Fields(rest)
	// fields[0] = state (field 3)
	// fields[11] = utime (field 14)
	// fields[12] = stime (field 15)
	if len(fields) < 13 {
		return '?', 0, 0
	}

	state := byte('?')
	if len(fields[0]) > 0 {
		state = fields[0][0]
	}

	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)

	return state, utime, stime
}

func formatThreads(threads []threadInfo, pid, maxShow int) string {
	if len(threads) == 0 {
		return fmt.Sprintf("Process %d has no threads (may have exited).", pid)
	}

	sort.Slice(threads, func(i, j int) bool {
		return threads[i].totalCPU() > threads[j].totalCPU()
	})

	var b strings.Builder
	total := len(threads)
	fmt.Fprintf(&b, "Process %d: %d threads\n\n", pid, total)

	showing := total
	if showing > maxShow {
		showing = maxShow
	}

	fmt.Fprintf(&b, "%-8s %-20s %-6s %12s %12s %12s  %s\n", "TID", "NAME", "STATE", "UTIME", "STIME", "TOTAL", "WCHAN")
	b.WriteString(strings.Repeat("-", 90))
	b.WriteString("\n")

	dStateCount := 0
	for i := 0; i < showing; i++ {
		t := threads[i]
		stateStr := string(t.state)
		marker := ""
		if t.state == 'D' {
			marker = " [!]"
			dStateCount++
		}
		wchanStr := t.wchan
		if wchanStr == "" {
			wchanStr = "-"
		}
		fmt.Fprintf(&b, "%-8d %-20s %-6s %12d %12d %12d  %s%s\n",
			t.tid, truncStr(t.comm, 20), stateStr, t.utime, t.stime, t.totalCPU(), wchanStr, marker)
	}

	if total > showing {
		fmt.Fprintf(&b, "  ... and %d more threads\n", total-showing)
	}

	if dStateCount > 0 {
		fmt.Fprintf(&b, "\n[!] %d thread(s) in D (uninterruptible sleep) state\n", dStateCount)
	}

	return b.String()
}
