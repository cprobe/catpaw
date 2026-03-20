package sysdiag

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/cmdx"
)

const (
	defaultCoredumpMax = 20
	maxCoredumpMax     = 100
	coredumpTimeout    = 10 * time.Second
)

var coredumpDirs = []string{
	"/var/lib/systemd/coredump",
	"/var/crash",
	"/var/core",
}

func registerCoredump(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_coredump", "sysdiag:coredump",
		"Coredump diagnostic tools (crash history, dump file listing). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_coredump", diagnose.DiagnoseTool{
		Name:        "coredump_list",
		Description: "List recent coredumps. Uses 'coredumpctl list' if available, otherwise scans /var/lib/systemd/coredump/ and /var/crash/.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "max_entries", Type: "string", Description: "Max entries to show (default 20, max 100)"},
		},
		Execute: execCoredumpList,
	})
}

func execCoredumpList(ctx context.Context, args map[string]string) (string, error) {
	maxEntries := defaultCoredumpMax
	if s := args["max_entries"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid max_entries %q: must be a positive integer", s)
		}
		if n > maxCoredumpMax {
			n = maxCoredumpMax
		}
		maxEntries = n
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("coredump_list requires linux (current: %s)", runtime.GOOS)
	}

	// Try coredumpctl first
	if result, err := tryCoredumpctl(ctx, maxEntries); err == nil {
		return result, nil
	}

	// Fallback: scan common directories
	return scanCoredumpDirs(maxEntries), nil
}

func tryCoredumpctl(ctx context.Context, maxEntries int) (string, error) {
	bin, err := exec.LookPath("coredumpctl")
	if err != nil {
		return "", err
	}

	cmd := exec.Command(bin, "list", "--no-pager", "-r")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	timeout := coredumpTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("coredumpctl timed out")
	}

	output := strings.TrimSpace(stdout.String())
	if runErr != nil && output == "" {
		return "", fmt.Errorf("coredumpctl failed: %v", runErr)
	}

	if output == "" || strings.Contains(output, "No coredumps found") {
		return "No coredumps found (via coredumpctl).", nil
	}

	lines := strings.Split(output, "\n")
	header := ""
	dataLines := lines

	if len(lines) > 0 && (strings.Contains(lines[0], "TIME") || strings.Contains(lines[0], "PID")) {
		header = lines[0]
		dataLines = lines[1:]
	}

	total := len(dataLines)
	if total > maxEntries {
		dataLines = dataLines[:maxEntries]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Coredumps (via coredumpctl): %d total", total)
	if total > maxEntries {
		fmt.Fprintf(&b, " (showing %d most recent)", maxEntries)
	}
	b.WriteString("\n\n")
	if header != "" {
		b.WriteString(header)
		b.WriteByte('\n')
	}
	for _, line := range dataLines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

type coredumpFile struct {
	path    string
	name    string
	size    int64
	modTime time.Time
}

func scanCoredumpDirs(maxEntries int) string {
	var files []coredumpFile

	for _, dir := range coredumpDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			name := entry.Name()
			ext := filepath.Ext(name)
			if ext == ".xz" || ext == ".lz4" || ext == ".zst" || ext == ".core" || ext == ".crash" || ext == "" {
				files = append(files, coredumpFile{
					path:    filepath.Join(dir, name),
					name:    name,
					size:    info.Size(),
					modTime: info.ModTime(),
				})
			}
		}
	}

	if len(files) == 0 {
		return "No coredumps found (scanned: " + strings.Join(coredumpDirs, ", ") + ")."
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	total := len(files)
	if total > maxEntries {
		files = files[:maxEntries]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Coredump files: %d found", total)
	if total > maxEntries {
		fmt.Fprintf(&b, " (showing %d most recent)", maxEntries)
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "%-20s  %10s  %s\n", "TIME", "SIZE", "FILE")
	b.WriteString(strings.Repeat("-", 70))
	b.WriteByte('\n')

	for _, f := range files {
		fmt.Fprintf(&b, "%-20s  %10s  %s\n",
			f.modTime.Format("2006-01-02 15:04:05"),
			humanBytes(uint64(f.size)),
			f.name)
	}
	return b.String()
}
