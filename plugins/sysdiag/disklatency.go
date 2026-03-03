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
	"time"

	"github.com/cprobe/catpaw/diagnose"
)

const (
	diskstatsPath    = "/proc/diskstats"
	diskstatsMaxRead = 128 * 1024
	diskSampleDelay  = time.Second
)

func registerDiskLatency(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_diskio", "sysdiag:diskio",
		"Disk IO latency tools (await, %util, IOPS from /proc/diskstats). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_diskio", diagnose.DiagnoseTool{
		Name:        "disk_io_latency",
		Description: "Sample /proc/diskstats twice and compute per-device IOPS, throughput (MB/s), average IO latency (await ms), and utilization (%util). Highlights devices with high await or utilization.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "delay_ms", Type: "string", Description: "Sampling delay in milliseconds (default: 1000, range: 500-5000)"},
			{Name: "show_idle", Type: "string", Description: "Set to 'true' to include devices with zero IO (default: hide idle)"},
		},
		Execute: execDiskLatency,
	})
}

// diskStats holds counters from one line of /proc/diskstats (kernel 4.18+ has 18+ fields).
// We use the classic 11 fields (kernel 2.6+).
type diskStats struct {
	name       string
	readsCompleted  uint64
	readsMerged     uint64
	sectorsRead     uint64
	readTimeMs      uint64
	writesCompleted uint64
	writesMerged    uint64
	sectorsWritten  uint64
	writeTimeMs     uint64
	ioInProgress    uint64
	ioTimeMs        uint64
	weightedIOMs    uint64
}

func (d *diskStats) totalIOs() uint64 {
	return d.readsCompleted + d.writesCompleted
}

type diskDelta struct {
	name        string
	readIOPS    float64
	writeIOPS   float64
	readMBs     float64
	writeMBs    float64
	awaitMs     float64
	util        float64
	ioInProgress uint64
}

func execDiskLatency(ctx context.Context, args map[string]string) (string, error) {
	delay := diskSampleDelay
	if v := strings.TrimSpace(args["delay_ms"]); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 500 || ms > 5000 {
			return "", fmt.Errorf("delay_ms must be 500-5000")
		}
		delay = time.Duration(ms) * time.Millisecond
	}

	showIdle := strings.ToLower(strings.TrimSpace(args["show_idle"])) == "true"

	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("disk_io_latency requires linux (current: %s)", runtime.GOOS)
	}

	snap1, err := readDiskStats(diskstatsPath)
	if err != nil {
		return "", err
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
	}

	snap2, err := readDiskStats(diskstatsPath)
	if err != nil {
		return "", err
	}

	deltas := computeDiskDeltas(snap1, snap2, delay)
	return formatDiskLatency(deltas, delay, showIdle), nil
}

func readDiskStats(path string) (map[string]*diskStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, diskstatsMaxRead))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	result := make(map[string]*diskStats)
	for _, line := range strings.Split(string(data), "\n") {
		ds := parseDiskStatsLine(line)
		if ds != nil {
			result[ds.name] = ds
		}
	}
	return result, nil
}

func parseDiskStatsLine(line string) *diskStats {
	fields := strings.Fields(line)
	if len(fields) < 14 {
		return nil
	}

	name := fields[2]
	if shouldSkipDisk(name) {
		return nil
	}

	nums := make([]uint64, 11)
	for i := 0; i < 11 && i+3 < len(fields); i++ {
		nums[i], _ = strconv.ParseUint(fields[i+3], 10, 64)
	}

	return &diskStats{
		name:            name,
		readsCompleted:  nums[0],
		readsMerged:     nums[1],
		sectorsRead:     nums[2],
		readTimeMs:      nums[3],
		writesCompleted: nums[4],
		writesMerged:    nums[5],
		sectorsWritten:  nums[6],
		writeTimeMs:     nums[7],
		ioInProgress:    nums[8],
		ioTimeMs:        nums[9],
		weightedIOMs:    nums[10],
	}
}

func shouldSkipDisk(name string) bool {
	if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
		return true
	}
	if strings.HasPrefix(name, "dm-") {
		return false
	}
	return false
}

func computeDiskDeltas(snap1, snap2 map[string]*diskStats, delay time.Duration) []diskDelta {
	secs := delay.Seconds()
	if secs <= 0 {
		secs = 1
	}

	var deltas []diskDelta
	for name, s2 := range snap2 {
		s1, ok := snap1[name]
		if !ok {
			continue
		}

		dReads := safeDelta(s2.readsCompleted, s1.readsCompleted)
		dWrites := safeDelta(s2.writesCompleted, s1.writesCompleted)
		dSectRead := safeDelta(s2.sectorsRead, s1.sectorsRead)
		dSectWrite := safeDelta(s2.sectorsWritten, s1.sectorsWritten)
		dReadMs := safeDelta(s2.readTimeMs, s1.readTimeMs)
		dWriteMs := safeDelta(s2.writeTimeMs, s1.writeTimeMs)
		dIOMs := safeDelta(s2.ioTimeMs, s1.ioTimeMs)

		totalIOs := dReads + dWrites
		totalMs := dReadMs + dWriteMs

		awaitMs := 0.0
		if totalIOs > 0 {
			awaitMs = float64(totalMs) / float64(totalIOs)
		}

		util := float64(dIOMs) / (secs * 1000) * 100
		if util > 100 {
			util = 100
		}

		deltas = append(deltas, diskDelta{
			name:         name,
			readIOPS:     float64(dReads) / secs,
			writeIOPS:    float64(dWrites) / secs,
			readMBs:      float64(dSectRead) * 512 / 1024 / 1024 / secs,
			writeMBs:     float64(dSectWrite) * 512 / 1024 / 1024 / secs,
			awaitMs:      awaitMs,
			util:         util,
			ioInProgress: s2.ioInProgress,
		})
	}

	sort.Slice(deltas, func(i, j int) bool {
		return deltas[i].util > deltas[j].util
	})

	return deltas
}

func formatDiskLatency(deltas []diskDelta, delay time.Duration, showIdle bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Disk IO Latency (sampled over %.1fs)\n", delay.Seconds())
	b.WriteString(strings.Repeat("=", 60))
	b.WriteString("\n\n")

	var active []diskDelta
	for _, d := range deltas {
		if showIdle || d.readIOPS > 0 || d.writeIOPS > 0 || d.ioInProgress > 0 {
			active = append(active, d)
		}
	}

	if len(active) == 0 {
		if showIdle {
			b.WriteString("No block devices found.\n")
		} else {
			b.WriteString("All devices idle during sampling period.\n")
			b.WriteString("(use show_idle=true to see all devices)\n")
		}
		return b.String()
	}

	fmt.Fprintf(&b, "%-10s %8s %8s %8s %8s %8s %6s %s\n",
		"DEVICE", "r/s", "w/s", "rMB/s", "wMB/s", "await", "%util", "FLAGS")
	b.WriteString(strings.Repeat("-", 75))
	b.WriteString("\n")

	const maxDevices = 50
	showing := len(active)
	if showing > maxDevices {
		showing = maxDevices
	}

	highAwait := 0
	highUtil := 0
	for i, d := range active {
		if i >= showing {
			break
		}
		flags := ""
		if d.awaitMs >= 100 {
			flags += " [!!!]"
			highAwait++
		} else if d.awaitMs >= 20 {
			flags += " [!]"
			highAwait++
		}
		if d.util >= 95 {
			flags += " SATURATED"
			highUtil++
		} else if d.util >= 80 {
			flags += " BUSY"
			highUtil++
		}
		if d.ioInProgress > 0 {
			flags += fmt.Sprintf(" (inflight=%d)", d.ioInProgress)
		}

		fmt.Fprintf(&b, "%-10s %8.1f %8.1f %8.2f %8.2f %7.1fms %5.1f%%%s\n",
			truncStr(d.name, 10),
			d.readIOPS, d.writeIOPS,
			d.readMBs, d.writeMBs,
			d.awaitMs, d.util, flags)
	}

	if len(active) > showing {
		fmt.Fprintf(&b, "  ... and %d more devices (showing top %d)\n", len(active)-showing, showing)
	}

	b.WriteString("\n")
	if highAwait > 0 {
		fmt.Fprintf(&b, "[!] %d device(s) with elevated IO latency (await >= 20ms)\n", highAwait)
	}
	if highUtil > 0 {
		fmt.Fprintf(&b, "[!] %d device(s) with high utilization (>= 80%%)\n", highUtil)
	}

	return b.String()
}
