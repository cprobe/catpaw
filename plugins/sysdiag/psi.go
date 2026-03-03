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
	psiBasePath    = "/proc/pressure"
	psiMaxFileSize = 4096
)

var psiResources = []string{"cpu", "memory", "io"}

func registerPSI(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_psi", "sysdiag:psi",
		"Pressure Stall Information tools (CPU/memory/IO pressure, Linux 4.20+).",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_psi", diagnose.DiagnoseTool{
		Name:        "psi_check",
		Description: "Show Pressure Stall Information for CPU, memory, and IO. Reports avg10/avg60/avg300 percentages for 'some' and 'full' stall categories. Requires Linux 4.20+.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "resource", Type: "string", Description: "Which resource: cpu, memory, io, or all (default: all)"},
		},
		Execute: execPSICheck,
	})
}

type psiLine struct {
	category string // "some" or "full"
	avg10    float64
	avg60    float64
	avg300   float64
	totalUs  uint64
}

type psiResult struct {
	resource string
	lines    []psiLine
	err      error
}

func execPSICheck(_ context.Context, args map[string]string) (string, error) {
	resource := "all"
	if r := strings.ToLower(strings.TrimSpace(args["resource"])); r != "" {
		switch r {
		case "cpu", "memory", "io", "all":
			resource = r
		default:
			return "", fmt.Errorf("invalid resource %q (valid: cpu, memory, io, all)", args["resource"])
		}
	}

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("psi_check requires linux (current: %s)", runtime.GOOS)
	}

	resources := psiResources
	if resource != "all" {
		resources = []string{resource}
	}

	results := make([]psiResult, 0, len(resources))
	for _, res := range resources {
		lines, err := readPSIFile(fmt.Sprintf("%s/%s", psiBasePath, res))
		results = append(results, psiResult{resource: res, lines: lines, err: err})
	}

	return formatPSI(results), nil
}

func readPSIFile(path string) ([]psiLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w (PSI requires Linux 4.20+)", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, psiMaxFileSize))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var lines []psiLine
	for _, raw := range strings.Split(string(data), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		pl, ok := parsePSILine(raw)
		if ok {
			lines = append(lines, pl)
		}
	}
	return lines, nil
}

// parsePSILine parses a line like:
// some avg10=0.00 avg60=0.00 avg300=0.00 total=0
// full avg10=1.23 avg60=0.45 avg300=0.12 total=123456
func parsePSILine(line string) (psiLine, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return psiLine{}, false
	}

	cat := fields[0]
	if cat != "some" && cat != "full" {
		return psiLine{}, false
	}

	pl := psiLine{category: cat}
	for _, field := range fields[1:] {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "avg10":
			pl.avg10, _ = strconv.ParseFloat(kv[1], 64)
		case "avg60":
			pl.avg60, _ = strconv.ParseFloat(kv[1], 64)
		case "avg300":
			pl.avg300, _ = strconv.ParseFloat(kv[1], 64)
		case "total":
			pl.totalUs, _ = strconv.ParseUint(kv[1], 10, 64)
		}
	}
	return pl, true
}

func formatPSI(results []psiResult) string {
	allErr := true
	for _, r := range results {
		if r.err == nil {
			allErr = false
			break
		}
	}
	if allErr {
		return fmt.Sprintf("PSI not available: %v", results[0].err)
	}

	var b strings.Builder
	b.WriteString("Pressure Stall Information (PSI)\n")
	b.WriteString("Values are % of time stalled in the last 10s / 60s / 300s\n\n")

	fmt.Fprintf(&b, "%-8s  %-5s  %8s  %8s  %8s  %14s\n",
		"RESOURCE", "TYPE", "AVG10", "AVG60", "AVG300", "TOTAL(us)")
	b.WriteString(strings.Repeat("-", 60))
	b.WriteByte('\n')

	hasWarning := false
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(&b, "%-8s  (error: %v)\n", r.resource, r.err)
			continue
		}
		if len(r.lines) == 0 {
			fmt.Fprintf(&b, "%-8s  (no data)\n", r.resource)
			continue
		}
		for _, pl := range r.lines {
			marker := ""
			if pl.avg10 >= 25.0 {
				marker = " [!!!]"
				hasWarning = true
			} else if pl.avg10 >= 10.0 {
				marker = " [!]"
				hasWarning = true
			}
			fmt.Fprintf(&b, "%-8s  %-5s  %7.2f%%  %7.2f%%  %7.2f%%  %14d%s\n",
				r.resource, pl.category, pl.avg10, pl.avg60, pl.avg300, pl.totalUs, marker)
		}
	}

	if hasWarning {
		b.WriteString("\n[!] = avg10 >= 10%  [!!!] = avg10 >= 25% (significant stall pressure)\n")
	}
	return b.String()
}
