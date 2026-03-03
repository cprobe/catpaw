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

const (
	softnetPath    = "/proc/net/softnet_stat"
	softnetMaxRead = 64 * 1024
)

func registerSoftnet(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_softnet", "sysdiag:softnet",
		"Softnet backlog diagnostic tools (per-CPU packet processing stats). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_softnet", diagnose.DiagnoseTool{
		Name:        "softnet_stat",
		Description: "Show per-CPU softnet statistics from /proc/net/softnet_stat: packets processed, dropped (backlog overflow), and time_squeeze (ran out of CPU budget). Non-zero drops or squeezes indicate packet loss at the network stack level.",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execSoftnetStat,
	})
}

type softnetCPU struct {
	cpu         int
	processed   uint64
	dropped     uint64
	timeSqueeze uint64
}

func execSoftnetStat(_ context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("softnet_stat requires linux (current: %s)", runtime.GOOS)
	}

	cpus, err := parseSoftnetStat(softnetPath)
	if err != nil {
		return "", err
	}

	return formatSoftnet(cpus), nil
}

func parseSoftnetStat(path string) ([]softnetCPU, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, softnetMaxRead))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cpus []softnetCPU
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		processed, _ := strconv.ParseUint(fields[0], 16, 64)
		dropped, _ := strconv.ParseUint(fields[1], 16, 64)
		timeSqueeze, _ := strconv.ParseUint(fields[2], 16, 64)

		cpus = append(cpus, softnetCPU{
			cpu:         i,
			processed:   processed,
			dropped:     dropped,
			timeSqueeze: timeSqueeze,
		})
	}
	return cpus, nil
}

const softnetMaxDisplay = 64

func formatSoftnet(cpus []softnetCPU) string {
	if len(cpus) == 0 {
		return "No softnet data available.\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Softnet Statistics (%d CPUs)\n", len(cpus))
	b.WriteString(strings.Repeat("=", 55))
	b.WriteString("\n\n")

	var totalProcessed, totalDropped, totalSqueeze uint64
	droppedCPUs := 0
	squeezeCPUs := 0

	for _, c := range cpus {
		totalProcessed += c.processed
		totalDropped += c.dropped
		totalSqueeze += c.timeSqueeze
		if c.dropped > 0 {
			droppedCPUs++
		}
		if c.timeSqueeze > 0 {
			squeezeCPUs++
		}
	}

	compactMode := len(cpus) > softnetMaxDisplay

	fmt.Fprintf(&b, "%-6s %14s %12s %14s  %s\n", "CPU", "PROCESSED", "DROPPED", "TIME_SQUEEZE", "FLAGS")
	b.WriteString(strings.Repeat("-", 60))
	b.WriteString("\n")

	displayed := 0
	for _, c := range cpus {
		if compactMode && c.dropped == 0 && c.timeSqueeze == 0 {
			continue
		}

		flags := ""
		if c.dropped > 0 {
			flags += " [!drop]"
		}
		if c.timeSqueeze > 0 {
			flags += " [!squeeze]"
		}

		fmt.Fprintf(&b, "%-6d %14d %12d %14d%s\n",
			c.cpu, c.processed, c.dropped, c.timeSqueeze, flags)
		displayed++
	}

	if compactMode && displayed < len(cpus) {
		fmt.Fprintf(&b, "  ... %d healthy CPUs hidden (showing only CPUs with drops/squeezes)\n", len(cpus)-displayed)
	}

	b.WriteString(strings.Repeat("-", 60))
	b.WriteString("\n")
	fmt.Fprintf(&b, "%-6s %14d %12d %14d\n", "TOTAL", totalProcessed, totalDropped, totalSqueeze)

	b.WriteString("\n")
	if totalDropped > 0 {
		fmt.Fprintf(&b, "[!] %d CPU(s) have non-zero drops (total: %d).\n", droppedCPUs, totalDropped)
		b.WriteString("    Packets were lost because the softnet backlog was full.\n")
		b.WriteString("    Consider increasing net.core.netdev_max_backlog.\n")
	}
	if totalSqueeze > 0 {
		fmt.Fprintf(&b, "[!] %d CPU(s) have non-zero time_squeeze (total: %d).\n", squeezeCPUs, totalSqueeze)
		b.WriteString("    CPU ran out of budget to process all packets.\n")
		b.WriteString("    Consider increasing net.core.netdev_budget.\n")
	}
	if totalDropped == 0 && totalSqueeze == 0 {
		b.WriteString("No drops or time squeezes detected. Network stack processing is healthy.\n")
	}

	return b.String()
}
