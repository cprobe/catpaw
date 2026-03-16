package disk

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
	godisk "github.com/shirou/gopsutil/v3/disk"
)

var _ plugins.Diagnosable = (*DiskPlugin)(nil)

func diskOverviewReport(ctx context.Context) string {
	partitions, err := godisk.PartitionsWithContext(ctx, true)
	if err != nil {
		return fmt.Sprintf("get partitions: %v", err)
	}

	var b strings.Builder

	fmt.Fprintf(&b, "[Space Usage]\n")
	fmt.Fprintf(&b, "%-30s %10s %10s %10s %6s  %s\n",
		"Filesystem", "Size", "Used", "Avail", "Use%", "Mounted on")
	for _, p := range partitions {
		usage, err := godisk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%-30s %10s %10s %10s %5.1f%%  %s\n",
			p.Device,
			humanBytes(usage.Total),
			humanBytes(usage.Used),
			humanBytes(usage.Free),
			usage.UsedPercent,
			p.Mountpoint)
	}

	fmt.Fprintf(&b, "\n[Partitions]\n")
	fmt.Fprintf(&b, "%-20s %-30s %-10s %s\n", "Device", "Mountpoint", "FSType", "Opts")
	for _, p := range partitions {
		opts := strings.Join(p.Opts, ",")
		if len(opts) > 60 {
			opts = opts[:57] + "..."
		}
		fmt.Fprintf(&b, "%-20s %-30s %-10s %s\n", p.Device, p.Mountpoint, p.Fstype, opts)
	}

	return b.String()
}

func (p *DiskPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("disk", "disk", "Disk diagnostic tools (overview, I/O stats)", diagnose.ToolScopeLocal)

	registry.Register("disk", diagnose.DiagnoseTool{
		Name:        "disk_overview",
		Description: "Combined disk snapshot: space usage per filesystem (size/used/avail/use%, like df -h) plus partition details (device, mountpoint, fstype, mount options). NOTE: pre-collected in system baseline — use only when refreshing.",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			return diskOverviewReport(ctx), nil
		},
	})

	registry.Register("disk", diagnose.DiagnoseTool{
		Name:        "disk_io_counters",
		Description: "Show disk I/O stats per device: type (HDD/SSD/NVMe), read/write counts and bytes, average latency, and current queue depth",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			counters, err := godisk.IOCountersWithContext(ctx)
			if err != nil {
				return "", fmt.Errorf("get io counters: %w", err)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%-12s %-6s %10s %10s %12s %12s %10s %10s %6s\n",
				"Device", "Type", "Reads", "Writes", "ReadBytes", "WriteBytes", "AvgRdMs", "AvgWrMs", "Queue")
			for name, c := range counters {
				devType := detectDeviceType(name)
				avgRd := avgLatency(c.ReadTime, c.ReadCount)
				avgWr := avgLatency(c.WriteTime, c.WriteCount)
				fmt.Fprintf(&b, "%-12s %-6s %10d %10d %12s %12s %10s %10s %6d\n",
					name, devType, c.ReadCount, c.WriteCount,
					humanBytes(c.ReadBytes), humanBytes(c.WriteBytes),
					avgRd, avgWr, c.IopsInProgress)
			}

			fmt.Fprintf(&b, "\nNotes:\n")
			fmt.Fprintf(&b, "  Type: HDD=rotational, SSD=solid-state, NVMe=NVM Express, ?=unknown\n")
			fmt.Fprintf(&b, "  AvgRdMs/AvgWrMs: average latency per I/O operation (cumulative since boot)\n")
			fmt.Fprintf(&b, "    - HDD normal: 5-15ms read, 5-15ms write\n")
			fmt.Fprintf(&b, "    - SSD normal: 0.1-1ms read, 0.1-2ms write\n")
			fmt.Fprintf(&b, "    - NVMe normal: 0.02-0.1ms read, 0.02-0.2ms write\n")
			fmt.Fprintf(&b, "  Queue: current I/O operations in flight (0 = idle)\n")
			return b.String(), nil
		},
	})

	registry.SetDiagnoseHints("disk", `
- 磁盘空间告警 → disk_overview（已包含使用率 + 分区信息，含 fstype 和挂载选项）
- I/O 延迟告警 → disk_io_counters 查看各设备读写延迟和队列深度
- 首轮建议并行调用 disk_overview + disk_io_counters，一次拿到空间和 I/O 全貌`)

	registry.RegisterPreCollector("disk", func(ctx context.Context, _ any) string {
		return diskOverviewReport(ctx)
	})
	registry.RegisterBaselinePlugin("disk")
}

func detectDeviceType(name string) string {
	if strings.HasPrefix(name, "nvme") {
		return "NVMe"
	}
	if runtime.GOOS != "linux" {
		return "?"
	}

	base := name
	for len(base) > 0 && base[len(base)-1] >= '0' && base[len(base)-1] <= '9' {
		base = base[:len(base)-1]
	}

	for _, candidate := range []string{name, base} {
		data, err := os.ReadFile("/sys/block/" + candidate + "/queue/rotational")
		if err != nil {
			continue
		}
		val := strings.TrimSpace(string(data))
		if val == "1" {
			return "HDD"
		}
		return "SSD"
	}
	return "?"
}

func avgLatency(totalMs, count uint64) string {
	if count == 0 {
		return "-"
	}
	avg := float64(totalMs) / float64(count)
	if avg < 0.01 {
		return "0.00"
	}
	return fmt.Sprintf("%.2f", avg)
}

func humanBytes(b uint64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
		tb = gb * 1024
	)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.1fT", float64(b)/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.1fG", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1fM", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1fK", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
