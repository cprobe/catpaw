package cpu

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/plugins"
	gocpu "github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/load"
	goprocess "github.com/shirou/gopsutil/v3/process"
)

var _ plugins.Diagnosable = (*CpuPlugin)(nil)

func (p *CpuPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("cpu", "cpu", "CPU diagnostic tools (usage, load, top processes)", diagnose.ToolScopeLocal)

	registry.Register("cpu", diagnose.DiagnoseTool{
		Name:        "cpu_usage",
		Description: "Show current CPU usage percentage (total and per-core)",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			total, err := gocpu.PercentWithContext(ctx, 0, false)
			if err != nil {
				return "", fmt.Errorf("get cpu percent: %w", err)
			}
			perCore, _ := gocpu.PercentWithContext(ctx, 0, true)

			var b strings.Builder
			cores, _ := gocpu.CountsWithContext(ctx, true)
			fmt.Fprintf(&b, "CPU cores: %d (logical)\n", cores)
			if len(total) > 0 {
				fmt.Fprintf(&b, "Total CPU usage: %.1f%%\n", total[0])
			}
			if len(perCore) > 0 {
				fmt.Fprintf(&b, "\nPer-core usage:\n")
				for i, pct := range perCore {
					fmt.Fprintf(&b, "  core %d: %.1f%%\n", i, pct)
				}
			}
			return b.String(), nil
		},
	})

	registry.Register("cpu", diagnose.DiagnoseTool{
		Name:        "cpu_load_average",
		Description: "Show system load averages (1min, 5min, 15min) and ratio to CPU cores",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			avg, err := load.AvgWithContext(ctx)
			if err != nil {
				return "", fmt.Errorf("get load avg: %w", err)
			}
			cores := runtime.NumCPU()
			var b strings.Builder
			fmt.Fprintf(&b, "CPU cores: %d\n", cores)
			fmt.Fprintf(&b, "Load average: %.2f (1m), %.2f (5m), %.2f (15m)\n",
				avg.Load1, avg.Load5, avg.Load15)
			fmt.Fprintf(&b, "Load/core:    %.2f (1m), %.2f (5m), %.2f (15m)\n",
				avg.Load1/float64(cores), avg.Load5/float64(cores), avg.Load15/float64(cores))
			return b.String(), nil
		},
	})

	registry.Register("cpu", diagnose.DiagnoseTool{
		Name:        "top_cpu_processes",
		Description: "Show top 10 processes by CPU usage (samples up to 1000 processes to limit overhead on stressed systems)",
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
				cpu  float64
				mem  float32
			}

			var infos []procInfo
			sampled := 0
			for _, p := range procs {
				if sampled >= maxSample {
					break
				}
				cpuPct, err := p.CPUPercentWithContext(ctx)
				if err != nil {
					continue
				}
				sampled++
				if cpuPct < 0.1 {
					continue
				}
				name, _ := p.NameWithContext(ctx)
				memPct, _ := p.MemoryPercentWithContext(ctx)
				infos = append(infos, procInfo{pid: p.Pid, name: name, cpu: cpuPct, mem: memPct})
			}

			for i := 0; i < len(infos) && i < 10; i++ {
				maxIdx := i
				for j := i + 1; j < len(infos); j++ {
					if infos[j].cpu > infos[maxIdx].cpu {
						maxIdx = j
					}
				}
				infos[i], infos[maxIdx] = infos[maxIdx], infos[i]
			}

			var b strings.Builder
			if len(procs) > maxSample {
				fmt.Fprintf(&b, "Total processes: %d (sampled first %d)\n\n", len(procs), maxSample)
			}
			fmt.Fprintf(&b, "%7s  %6s  %6s  %s\n", "PID", "CPU%", "MEM%", "Name")
			limit := len(infos)
			if limit > 10 {
				limit = 10
			}
			for i := 0; i < limit; i++ {
				fmt.Fprintf(&b, "%7d  %5.1f%%  %5.1f%%  %s\n",
					infos[i].pid, infos[i].cpu, infos[i].mem, infos[i].name)
			}
			return b.String(), nil
		},
	})
}
