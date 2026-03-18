package mem

import (
	"context"
	"fmt"
	"strings"

	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/plugins"
	gomem "github.com/shirou/gopsutil/v3/mem"
	goprocess "github.com/shirou/gopsutil/v3/process"
)

var _ plugins.Diagnosable = (*MemPlugin)(nil)

func (p *MemPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("mem", "mem", "Memory diagnostic tools (usage, swap, top processes)", diagnose.ToolScopeLocal)

	registry.Register("mem", diagnose.DiagnoseTool{
		Name:        "mem_usage",
		Description: "Show system memory usage (total, used, available, buffers, cached, usage percent)",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			v, err := gomem.VirtualMemoryWithContext(ctx)
			if err != nil {
				return "", fmt.Errorf("get virtual memory: %w", err)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Total:     %s\n", humanBytes(v.Total))
			fmt.Fprintf(&b, "Used:      %s (%.1f%%)\n", humanBytes(v.Used), v.UsedPercent)
			fmt.Fprintf(&b, "Available: %s\n", humanBytes(v.Available))
			fmt.Fprintf(&b, "Buffers:   %s\n", humanBytes(v.Buffers))
			fmt.Fprintf(&b, "Cached:    %s\n", humanBytes(v.Cached))
			fmt.Fprintf(&b, "Free:      %s\n", humanBytes(v.Free))
			return b.String(), nil
		},
	})

	registry.Register("mem", diagnose.DiagnoseTool{
		Name:        "swap_usage",
		Description: "Show swap memory usage (total, used, free, usage percent)",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			s, err := gomem.SwapMemoryWithContext(ctx)
			if err != nil {
				return "", fmt.Errorf("get swap memory: %w", err)
			}
			if s.Total == 0 {
				return "Swap: not configured (total = 0)\n", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Total: %s\n", humanBytes(s.Total))
			fmt.Fprintf(&b, "Used:  %s (%.1f%%)\n", humanBytes(s.Used), s.UsedPercent)
			fmt.Fprintf(&b, "Free:  %s\n", humanBytes(s.Free))
			return b.String(), nil
		},
	})

	registry.Register("mem", diagnose.DiagnoseTool{
		Name:        "top_mem_processes",
		Description: "Show top 10 processes by memory usage (samples up to 1000 processes to limit overhead on stressed systems)",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			procs, err := goprocess.ProcessesWithContext(ctx)
			if err != nil {
				return "", fmt.Errorf("get processes: %w", err)
			}

			const maxSample = 1000
			type procInfo struct {
				pid  int32
				name string
				mem  float32
				rss  uint64
			}

			var infos []procInfo
			sampled := 0
			for _, p := range procs {
				if sampled >= maxSample {
					break
				}
				memPct, err := p.MemoryPercentWithContext(ctx)
				if err != nil || memPct < 0.1 {
					sampled++
					continue
				}
				sampled++
				name, _ := p.NameWithContext(ctx)
				var rss uint64
				if mi, err := p.MemoryInfoWithContext(ctx); err == nil && mi != nil {
					rss = mi.RSS
				}
				infos = append(infos, procInfo{pid: p.Pid, name: name, mem: memPct, rss: rss})
			}

			for i := 0; i < len(infos) && i < 10; i++ {
				maxIdx := i
				for j := i + 1; j < len(infos); j++ {
					if infos[j].mem > infos[maxIdx].mem {
						maxIdx = j
					}
				}
				infos[i], infos[maxIdx] = infos[maxIdx], infos[i]
			}

			var b strings.Builder
			if len(procs) > maxSample {
				fmt.Fprintf(&b, "Total processes: %d (sampled first %d)\n\n", len(procs), maxSample)
			}
			fmt.Fprintf(&b, "%7s  %6s  %10s  %s\n", "PID", "MEM%", "RSS", "Name")
			limit := len(infos)
			if limit > 10 {
				limit = 10
			}
			for i := 0; i < limit; i++ {
				fmt.Fprintf(&b, "%7d  %5.1f%%  %10s  %s\n",
					infos[i].pid, infos[i].mem, humanBytes(uint64(infos[i].rss)), infos[i].name)
			}
			return b.String(), nil
		},
	})
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
