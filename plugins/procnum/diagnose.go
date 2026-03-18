package procnum

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cprobe/digcore/diagnose"
	"github.com/cprobe/digcore/plugins"
	"github.com/shirou/gopsutil/v3/process"
)

var _ plugins.Diagnosable = (*ProcnumPlugin)(nil)

func (p *ProcnumPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("process", "procnum",
		"Process diagnostic tools (list, search, detail)", diagnose.ToolScopeLocal)

	registry.Register("process", diagnose.DiagnoseTool{
		Name:        "process_list",
		Description: "List processes sorted by CPU usage (top 20). Samples up to 1000 processes to limit overhead on stressed systems.",
		Scope:       diagnose.ToolScopeLocal,
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			procs, err := process.ProcessesWithContext(ctx)
			if err != nil {
				return "", fmt.Errorf("list processes: %w", err)
			}

			const maxSample = 1000
			type info struct {
				pid  int32
				name string
				user string
				cpu  float64
				mem  float32
				cmd  string
			}

			var all []info
			sampled := 0
			for _, p := range procs {
				if sampled >= maxSample {
					break
				}
				sampled++
				cpuPct, err := p.CPUPercentWithContext(ctx)
				if err != nil {
					continue
				}
				name, _ := p.NameWithContext(ctx)
				user, _ := p.UsernameWithContext(ctx)
				memPct, _ := p.MemoryPercentWithContext(ctx)
				cmd, _ := p.CmdlineWithContext(ctx)
				if len(cmd) > 120 {
					cmd = cmd[:120] + "..."
				}
				all = append(all, info{pid: p.Pid, name: name, user: user, cpu: cpuPct, mem: memPct, cmd: cmd})
			}

			sort.Slice(all, func(i, j int) bool { return all[i].cpu > all[j].cpu })

			var b strings.Builder
			if len(procs) > maxSample {
				fmt.Fprintf(&b, "Total processes: %d (sampled first %d)\n\n", len(procs), maxSample)
			} else {
				fmt.Fprintf(&b, "Total processes: %d\n\n", len(all))
			}
			fmt.Fprintf(&b, "%7s  %6s  %6s  %-12s  %-16s  %s\n", "PID", "CPU%", "MEM%", "USER", "NAME", "CMDLINE")
			limit := 20
			if limit > len(all) {
				limit = len(all)
			}
			for i := 0; i < limit; i++ {
				p := all[i]
				fmt.Fprintf(&b, "%7d  %5.1f%%  %5.1f%%  %-12s  %-16s  %s\n",
					p.pid, p.cpu, p.mem, truncate(p.user, 12), truncate(p.name, 16), p.cmd)
			}
			return b.String(), nil
		},
	})

	registry.Register("process", diagnose.DiagnoseTool{
		Name:        "process_search",
		Description: "Search processes by name or command line substring. Lightweight: only reads name+cmdline per process, skips heavy stats for non-matches. Parameter: pattern (required)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "pattern", Type: "string", Description: "Substring to match against process name or command line", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			pattern := args["pattern"]
			if pattern == "" {
				return "", fmt.Errorf("parameter 'pattern' is required")
			}
			procs, err := process.ProcessesWithContext(ctx)
			if err != nil {
				return "", fmt.Errorf("list processes: %w", err)
			}

			lowerPattern := strings.ToLower(pattern)
			var b strings.Builder
			count := 0
			fmt.Fprintf(&b, "%7s  %6s  %6s  %-12s  %-16s  %s\n", "PID", "CPU%", "MEM%", "USER", "NAME", "CMDLINE")
			for _, p := range procs {
				name, _ := p.NameWithContext(ctx)
				cmd, _ := p.CmdlineWithContext(ctx)
				if !strings.Contains(strings.ToLower(name), lowerPattern) &&
					!strings.Contains(strings.ToLower(cmd), lowerPattern) {
					continue
				}
				cpuPct, _ := p.CPUPercentWithContext(ctx)
				user, _ := p.UsernameWithContext(ctx)
				memPct, _ := p.MemoryPercentWithContext(ctx)
				if len(cmd) > 120 {
					cmd = cmd[:120] + "..."
				}
				fmt.Fprintf(&b, "%7d  %5.1f%%  %5.1f%%  %-12s  %-16s  %s\n",
					p.Pid, cpuPct, memPct, truncate(user, 12), truncate(name, 16), cmd)
				count++
				if count >= 50 {
					fmt.Fprintf(&b, "\n... (showing first 50 matches)")
					break
				}
			}
			if count == 0 {
				return fmt.Sprintf("No processes found matching pattern: %s", pattern), nil
			}
			return fmt.Sprintf("Found %d processes matching '%s':\n\n%s", count, pattern, b.String()), nil
		},
	})

	registry.Register("process", diagnose.DiagnoseTool{
		Name:        "process_detail",
		Description: "Show detailed info for a specific process by PID: status, memory, open files, connections, threads. Parameter: pid (required)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "pid", Type: "int", Description: "Process ID to inspect", Required: true},
		},
		Execute: func(ctx context.Context, args map[string]string) (string, error) {
			pidStr := args["pid"]
			if pidStr == "" {
				return "", fmt.Errorf("parameter 'pid' is required")
			}
			var pid int32
			if _, err := fmt.Sscanf(pidStr, "%d", &pid); err != nil {
				return "", fmt.Errorf("invalid pid: %s", pidStr)
			}

			p, err := process.NewProcessWithContext(ctx, pid)
			if err != nil {
				return "", fmt.Errorf("process %d not found: %w", pid, err)
			}

			var b strings.Builder
			name, _ := p.NameWithContext(ctx)
			cmd, _ := p.CmdlineWithContext(ctx)
			status, _ := p.StatusWithContext(ctx)
			user, _ := p.UsernameWithContext(ctx)
			ppid, _ := p.PpidWithContext(ctx)
			cpuPct, _ := p.CPUPercentWithContext(ctx)
			memPct, _ := p.MemoryPercentWithContext(ctx)
			memInfo, _ := p.MemoryInfoWithContext(ctx)
			threads, _ := p.NumThreadsWithContext(ctx)
			fds, _ := p.NumFDsWithContext(ctx)
			createTime, _ := p.CreateTimeWithContext(ctx)

			fmt.Fprintf(&b, "PID:        %d\n", pid)
			fmt.Fprintf(&b, "Name:       %s\n", name)
			fmt.Fprintf(&b, "Status:     %v\n", status)
			fmt.Fprintf(&b, "User:       %s\n", user)
			fmt.Fprintf(&b, "PPID:       %d\n", ppid)
			fmt.Fprintf(&b, "CPU%%:       %.1f%%\n", cpuPct)
			fmt.Fprintf(&b, "MEM%%:       %.1f%%\n", memPct)
			if memInfo != nil {
				fmt.Fprintf(&b, "RSS:        %s\n", humanBytes(memInfo.RSS))
				fmt.Fprintf(&b, "VMS:        %s\n", humanBytes(memInfo.VMS))
			}
			fmt.Fprintf(&b, "Threads:    %d\n", threads)
			fmt.Fprintf(&b, "FDs:        %d\n", fds)
			if createTime > 0 {
				fmt.Fprintf(&b, "CreateTime: %d (epoch ms)\n", createTime)
			}
			fmt.Fprintf(&b, "Cmdline:    %s\n", cmd)

			conns, err := p.ConnectionsWithContext(ctx)
			if err == nil && len(conns) > 0 {
				fmt.Fprintf(&b, "\nNetwork connections (%d):\n", len(conns))
				limit := 20
				if limit > len(conns) {
					limit = len(conns)
				}
				for i := 0; i < limit; i++ {
					c := conns[i]
					fmt.Fprintf(&b, "  %s %s:%d → %s:%d (%s)\n",
						connType(c.Type), c.Laddr.IP, c.Laddr.Port,
						c.Raddr.IP, c.Raddr.Port, c.Status)
				}
				if len(conns) > limit {
					fmt.Fprintf(&b, "  ... and %d more\n", len(conns)-limit)
				}
			}
			return b.String(), nil
		},
	})
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func connType(t uint32) string {
	switch t {
	case 1:
		return "tcp"
	case 2:
		return "udp"
	default:
		return fmt.Sprintf("type%d", t)
	}
}
