package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

// baselineTools lists tools to run at chat startup for the system snapshot.
// Only local, no-arg (or safe-default) tools should appear here.
var baselineTools = []baselineDef{
	{name: "cpu_usage", label: "CPU"},
	{name: "cpu_load_average", label: "Load"},
	{name: "mem_usage", label: "Memory"},
	{name: "swap_usage", label: "Swap"},
	{name: "disk_usage", label: "Disk"},
	{name: "filefd_usage", label: "File Descriptors"},
	{name: "psi_check", label: "PSI"},
}

type baselineDef struct {
	name  string
	label string
}

type snapshotResult struct {
	label  string
	output string
}

const (
	snapshotTimeout    = 15 * time.Second
	perToolTimeout     = 10 * time.Second
	maxSnapshotBytes   = 8000
	maxPerToolBytes    = 2000
)

// CollectSnapshot runs baseline diagnostic tools concurrently and returns
// a compact text summary. Failures are silently skipped.
func CollectSnapshot(registry *diagnose.ToolRegistry) string {
	results := make([]snapshotResult, len(baselineTools))
	var wg sync.WaitGroup

	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()

	for i, def := range baselineTools {
		tool, ok := registry.Get(def.name)
		if !ok || tool.Execute == nil {
			continue
		}
		if tool.Scope == diagnose.ToolScopeRemote {
			continue
		}

		wg.Add(1)
		go func(idx int, t *diagnose.DiagnoseTool, label string) {
			defer wg.Done()
			out := runBaselineTool(ctx, t)
			if out != "" {
				results[idx] = snapshotResult{label: label, output: out}
			}
		}(i, tool, def.label)
	}

	wg.Wait()

	var b strings.Builder
	totalLen := 0
	for _, r := range results {
		if r.output == "" {
			continue
		}
		section := fmt.Sprintf("### %s\n%s\n", r.label, r.output)
		if totalLen+len(section) > maxSnapshotBytes {
			break
		}
		b.WriteString(section)
		totalLen += len(section)
	}

	return strings.TrimSpace(b.String())
}

func runBaselineTool(parent context.Context, tool *diagnose.DiagnoseTool) string {
	ctx, cancel := context.WithTimeout(parent, perToolTimeout)
	defer cancel()

	ch := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- ""
			}
		}()
		out, err := tool.Execute(ctx, nil)
		if err != nil {
			ch <- ""
			return
		}
		ch <- out
	}()

	var output string
	select {
	case output = <-ch:
	case <-ctx.Done():
		return ""
	}

	if output == "" {
		return ""
	}
	if !utf8.ValidString(output) {
		return ""
	}
	if len(output) > maxPerToolBytes {
		output = diagnose.TruncateUTF8(output, maxPerToolBytes) + "\n...[truncated]"
	}
	return strings.TrimSpace(output)
}
