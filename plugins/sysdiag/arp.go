package sysdiag

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/cprobe/catpaw/diagnose"
)

const (
	arpPath       = "/proc/net/arp"
	arpMaxSize    = 512 * 1024
	neighThresh3  = "/proc/sys/net/ipv4/neigh/default/gc_thresh3"
)

func registerARP(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_arp", "sysdiag:arp",
		"ARP/neighbor table diagnostic tools (entry count, stale/incomplete detection). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_arp", diagnose.DiagnoseTool{
		Name:        "arp_neigh",
		Description: "Show ARP/neighbor table summary: total entries, per-interface breakdown, incomplete entries. Compares against gc_thresh3 limit.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "show_all", Type: "string", Description: "Set to 'true' to list individual ARP entries (default: summary only)"},
		},
		Execute: execARPNeigh,
	})
}

type arpEntry struct {
	ip     string
	hwType string
	flags  string
	hwAddr string
	mask   string
	device string
}

func (e *arpEntry) isIncomplete() bool {
	return e.hwAddr == "00:00:00:00:00:00" || e.flags == "0x0"
}

func execARPNeigh(_ context.Context, args map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("arp_neigh requires linux (current: %s)", runtime.GOOS)
	}

	showAll := strings.ToLower(args["show_all"]) == "true"

	entries, truncated, err := parseARPTable(arpPath)
	if err != nil {
		return "", err
	}

	thresh3, _ := readUint64File(neighThresh3)

	return formatARP(entries, thresh3, showAll, truncated), nil
}

func parseARPTable(path string) ([]arpEntry, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	lr := io.LimitReader(f, arpMaxSize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	truncated := len(data) > arpMaxSize
	if truncated {
		data = data[:arpMaxSize]
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return []arpEntry{}, false, nil
	}

	var entries []arpEntry
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		entries = append(entries, arpEntry{
			ip:     fields[0],
			hwType: fields[1],
			flags:  fields[2],
			hwAddr: fields[3],
			mask:   fields[4],
			device: fields[5],
		})
	}
	return entries, truncated, nil
}

func formatARP(entries []arpEntry, thresh3 uint64, showAll, truncated bool) string {
	if len(entries) == 0 {
		return "ARP table is empty."
	}

	// Per-device summary
	type devStats struct {
		total      int
		incomplete int
	}
	perDev := make(map[string]*devStats)
	totalIncomplete := 0

	for _, e := range entries {
		ds, ok := perDev[e.device]
		if !ok {
			ds = &devStats{}
			perDev[e.device] = ds
		}
		ds.total++
		if e.isIncomplete() {
			ds.incomplete++
			totalIncomplete++
		}
	}

	var b strings.Builder
	total := len(entries)

	fmt.Fprintf(&b, "ARP/Neighbor Table: %d entries", total)
	if thresh3 > 0 {
		pct := float64(total) / float64(thresh3) * 100
		marker := ""
		if pct >= 90 {
			marker = " [!!!]"
		} else if pct >= 75 {
			marker = " [!]"
		}
		fmt.Fprintf(&b, " (gc_thresh3=%d, usage=%.1f%%%s)", thresh3, pct, marker)
	}
	if totalIncomplete > 0 {
		fmt.Fprintf(&b, " [!] %d incomplete", totalIncomplete)
	}
	b.WriteString("\n")
	if truncated {
		b.WriteString("  (output truncated, ARP table exceeds read limit)\n")
	}
	b.WriteString("\n")

	// Per-device breakdown
	b.WriteString("Per-interface:\n")
	devNames := make([]string, 0, len(perDev))
	for name := range perDev {
		devNames = append(devNames, name)
	}
	sort.Strings(devNames)

	for _, name := range devNames {
		ds := perDev[name]
		incNote := ""
		if ds.incomplete > 0 {
			incNote = fmt.Sprintf(" (%d incomplete)", ds.incomplete)
		}
		fmt.Fprintf(&b, "  %-16s  %d entries%s\n", name, ds.total, incNote)
	}

	if showAll {
		b.WriteString("\nAll entries:\n")
		fmt.Fprintf(&b, "  %-16s  %-18s  %-6s  %s\n", "IP", "MAC", "FLAGS", "DEV")
		b.WriteString("  " + strings.Repeat("-", 60) + "\n")

		for _, e := range entries {
			marker := ""
			if e.isIncomplete() {
				marker = " [incomplete]"
			}
			fmt.Fprintf(&b, "  %-16s  %-18s  %-6s  %s%s\n",
				e.ip, e.hwAddr, e.flags, e.device, marker)
		}
	}

	return b.String()
}
