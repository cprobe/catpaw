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
	interruptsPath    = "/proc/interrupts"
	interruptsMaxSize = 512 * 1024
	defaultIRQTop     = 20
	maxIRQTop         = 100
)

func registerInterrupts(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_irq", "sysdiag:irq",
		"Interrupt diagnostic tools (IRQ distribution, imbalance detection). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_irq", diagnose.DiagnoseTool{
		Name:        "interrupts",
		Description: "Show top interrupt sources from /proc/interrupts, sorted by total count. Reports per-CPU distribution imbalance for network and high-rate IRQs.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "top", Type: "string", Description: "Number of top IRQs to show (default 20, max 100)"},
		},
		Execute: execInterrupts,
	})
}

type irqEntry struct {
	name     string
	desc     string
	total    uint64
	perCPU   []uint64
	numCPUs  int
	maxCPU   int    // cpu index with highest count
	maxCount uint64 // count on that cpu
	minCount uint64 // count on cpu with lowest count (among active)
}

func execInterrupts(_ context.Context, args map[string]string) (string, error) {
	top := defaultIRQTop
	if s := args["top"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid top %q: must be a positive integer", s)
		}
		if n > maxIRQTop {
			n = maxIRQTop
		}
		top = n
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("interrupts requires linux (current: %s)", runtime.GOOS)
	}

	entries, numCPUs, err := parseInterrupts(interruptsPath)
	if err != nil {
		return "", err
	}

	return formatInterrupts(entries, numCPUs, top), nil
}

func parseInterrupts(path string) ([]irqEntry, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, interruptsMaxSize))
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return nil, 0, fmt.Errorf("unexpected /proc/interrupts format")
	}

	// First line: CPU0 CPU1 CPU2 ...
	cpuHeaders := strings.Fields(lines[0])
	numCPUs := len(cpuHeaders)
	if numCPUs == 0 {
		return nil, 0, fmt.Errorf("no CPU columns found in /proc/interrupts")
	}

	entries := make([]irqEntry, 0, len(lines)-1)
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		e, ok := parseIRQLine(line, numCPUs)
		if ok {
			entries = append(entries, e)
		}
	}
	return entries, numCPUs, nil
}

func parseIRQLine(line string, numCPUs int) (irqEntry, bool) {
	// Format: "  IRQ_NAME:  count0 count1 ... description text"
	colonIdx := strings.IndexByte(line, ':')
	if colonIdx < 0 {
		return irqEntry{}, false
	}

	name := strings.TrimSpace(line[:colonIdx])
	rest := strings.TrimSpace(line[colonIdx+1:])
	fields := strings.Fields(rest)

	if len(fields) < numCPUs {
		return irqEntry{}, false
	}

	perCPU := make([]uint64, numCPUs)
	var total uint64
	var maxCount, minCount uint64
	maxCPU := 0
	first := true

	for i := 0; i < numCPUs; i++ {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			return irqEntry{}, false
		}
		perCPU[i] = v
		total += v
		if v > maxCount {
			maxCount = v
			maxCPU = i
		}
		if first || v < minCount {
			minCount = v
			first = false
		}
	}

	desc := ""
	if len(fields) > numCPUs {
		desc = strings.Join(fields[numCPUs:], " ")
	}

	return irqEntry{
		name:     name,
		desc:     desc,
		total:    total,
		perCPU:   perCPU,
		numCPUs:  numCPUs,
		maxCPU:   maxCPU,
		maxCount: maxCount,
		minCount: minCount,
	}, true
}

func formatInterrupts(entries []irqEntry, numCPUs, top int) string {
	if len(entries) == 0 {
		return "No interrupt data found."
	}

	// Sort by total count descending
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].total > entries[j].total
	})

	if top > len(entries) {
		top = len(entries)
	}
	shown := entries[:top]

	var b strings.Builder
	fmt.Fprintf(&b, "Interrupt distribution (%d CPUs, showing top %d of %d IRQs)\n\n", numCPUs, top, len(entries))

	fmt.Fprintf(&b, "%-12s  %14s  %8s  %-6s  %s\n", "IRQ", "TOTAL", "IMBAL", "HOTCPU", "DESCRIPTION")
	b.WriteString(strings.Repeat("-", 75))
	b.WriteByte('\n')

	for _, e := range shown {
		imbal := ""
		hotcpu := ""
		if e.total > 0 && numCPUs > 1 {
			// Imbalance ratio: if one CPU handles much more than average
			avg := float64(e.total) / float64(numCPUs)
			if avg > 0 {
				ratio := float64(e.maxCount) / avg
				if ratio >= 2.0 {
					imbal = fmt.Sprintf("%.1fx", ratio)
					hotcpu = fmt.Sprintf("CPU%d", e.maxCPU)
				}
			}
		}
		desc := e.desc
		if len(desc) > 35 {
			desc = desc[:32] + "..."
		}
		fmt.Fprintf(&b, "%-12s  %14d  %8s  %-6s  %s\n", e.name, e.total, imbal, hotcpu, desc)
	}

	return b.String()
}
