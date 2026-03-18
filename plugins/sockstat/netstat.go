package sockstat

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
	snmpPath    = "/proc/net/snmp"
	snmpMaxSize = 64 * 1024 // 64KB; /proc/net/snmp is typically <4KB
)

// TCP counters from /proc/net/snmp that are most useful for diagnosing issues.
var tcpInteresting = map[string]bool{
	"RtoMin":       true,
	"RtoMax":       true,
	"MaxConn":      true,
	"ActiveOpens":  true,
	"PassiveOpens": true,
	"AttemptFails": true,
	"EstabResets":  true,
	"CurrEstab":    true,
	"InSegs":       true,
	"OutSegs":      true,
	"RetransSegs":  true,
	"InErrs":       true,
	"OutRsts":      true,
	"InCsumErrors": true,
}

// UDP counters from /proc/net/snmp.
var udpInteresting = map[string]bool{
	"InDatagrams":   true,
	"NoPorts":       true,
	"InErrors":      true,
	"OutDatagrams": true,
	"RcvbufErrors": true,
	"SndbufErrors": true,
	"InCsumErrors": true,
	"IgnoredMulti": true,
}

func registerNetstatSummary(registry *diagnose.ToolRegistry) {
	registry.Register("sockstat", diagnose.DiagnoseTool{
		Name:        "netstat_summary",
		Description: "Show key TCP and UDP counters from /proc/net/snmp (retransmits, errors, resets, connection stats). Complements sockstat_summary which shows TcpExt counters.",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execNetstatSummary,
	})
}

func execNetstatSummary(_ context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("netstat_summary requires linux (current: %s)", runtime.GOOS)
	}

	sections, err := parseSNMP(snmpPath)
	if err != nil {
		return "", err
	}

	var b strings.Builder

	if tcp, ok := sections["Tcp"]; ok {
		b.WriteString("TCP counters (cumulative since boot):\n\n")
		writeCounters(&b, tcp, tcpInteresting)
	}

	if udp, ok := sections["Udp"]; ok {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("UDP counters (cumulative since boot):\n\n")
		writeCounters(&b, udp, udpInteresting)
	}

	if b.Len() == 0 {
		return "No Tcp/Udp sections found in " + snmpPath, nil
	}

	return b.String(), nil
}

// parseSNMP reads path (typically /proc/net/snmp) and returns sections as map[sectionName]map[key]value.
// The file format is pairs of lines: "Tcp: header1 header2 ..." followed by "Tcp: val1 val2 ...".
// Reads are limited to snmpMaxSize bytes to avoid unbounded allocation on malformed or symlinked files.
func parseSNMP(path string) (map[string]map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, snmpMaxSize))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	sections := make(map[string]map[string]uint64)

	for i := 0; i+1 < len(lines); i += 2 {
		headerLine := lines[i]
		valueLine := lines[i+1]

		headerPrefix := sectionPrefix(headerLine)
		valuePrefix := sectionPrefix(valueLine)
		if headerPrefix == "" || headerPrefix != valuePrefix {
			continue
		}

		headers := strings.Fields(headerLine)
		values := strings.Fields(valueLine)
		if len(headers) != len(values) {
			continue
		}

		name := strings.TrimSuffix(headerPrefix, ":")
		if _, exists := sections[name]; exists {
			continue
		}

		counters := make(map[string]uint64)
		for j := 1; j < len(headers); j++ {
			v, err := strconv.ParseUint(values[j], 10, 64)
			if err != nil {
				continue
			}
			counters[headers[j]] = v
		}
		sections[name] = counters
	}
	return sections, nil
}

func sectionPrefix(line string) string {
	idx := strings.IndexByte(line, ' ')
	if idx < 0 {
		return ""
	}
	return line[:idx]
}

func writeCounters(b *strings.Builder, counters map[string]uint64, interesting map[string]bool) {
	keys := make([]string, 0, len(interesting))
	maxKeyLen := 0
	for k := range counters {
		if !interesting[k] {
			continue
		}
		keys = append(keys, k)
		if len(k) > maxKeyLen {
			maxKeyLen = len(k)
		}
	}
	sort.Strings(keys)

	fmtStr := fmt.Sprintf("  %%-%ds  %%d\n", maxKeyLen)
	for _, k := range keys {
		fmt.Fprintf(b, fmtStr, k, counters[k])
	}
}
