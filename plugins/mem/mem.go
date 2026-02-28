package mem

import (
	"fmt"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/conv"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/shirou/gopsutil/v3/mem"
)

const pluginName = "mem"

type MemoryUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type SwapUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	MemoryUsage MemoryUsageCheck `toml:"memory_usage"`
	SwapUsage   SwapUsageCheck   `toml:"swap_usage"`
}

type MemPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *MemPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &MemPlugin{}
	})
}

func (ins *Instance) Init() error {
	if ins.MemoryUsage.WarnGe > 100 || ins.MemoryUsage.CriticalGe > 100 {
		return fmt.Errorf("memory_usage thresholds must be between 0 and 100 (got warn_ge=%.1f, critical_ge=%.1f)",
			ins.MemoryUsage.WarnGe, ins.MemoryUsage.CriticalGe)
	}

	if ins.SwapUsage.WarnGe > 100 || ins.SwapUsage.CriticalGe > 100 {
		return fmt.Errorf("swap_usage thresholds must be between 0 and 100 (got warn_ge=%.1f, critical_ge=%.1f)",
			ins.SwapUsage.WarnGe, ins.SwapUsage.CriticalGe)
	}

	if ins.MemoryUsage.WarnGe > 0 && ins.MemoryUsage.CriticalGe > 0 &&
		ins.MemoryUsage.WarnGe >= ins.MemoryUsage.CriticalGe {
		return fmt.Errorf("memory_usage.warn_ge(%.1f) must be less than memory_usage.critical_ge(%.1f)",
			ins.MemoryUsage.WarnGe, ins.MemoryUsage.CriticalGe)
	}

	if ins.SwapUsage.WarnGe > 0 && ins.SwapUsage.CriticalGe > 0 &&
		ins.SwapUsage.WarnGe >= ins.SwapUsage.CriticalGe {
		return fmt.Errorf("swap_usage.warn_ge(%.1f) must be less than swap_usage.critical_ge(%.1f)",
			ins.SwapUsage.WarnGe, ins.SwapUsage.CriticalGe)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	ins.checkMemoryUsage(q)
	ins.checkSwapUsage(q)
}

func (ins *Instance) checkMemoryUsage(q *safe.Queue[*types.Event]) {
	if ins.MemoryUsage.WarnGe == 0 && ins.MemoryUsage.CriticalGe == 0 {
		return
	}

	vm, err := mem.VirtualMemory()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "mem::memory_usage",
			"target": "memory",
		}).SetTitleRule("[check]").
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to get memory info: %v", err)))
		return
	}

	tr := ins.MemoryUsage.TitleRule
	if tr == "" {
		tr = "[check]"
	}

	event := types.BuildEvent(map[string]string{
		"check":                            "mem::memory_usage",
		"target":                           "memory",
		types.AttrPrefix + "total":         conv.HumanBytes(vm.Total),
		types.AttrPrefix + "used":          conv.HumanBytes(vm.Used),
		types.AttrPrefix + "available":     conv.HumanBytes(vm.Available),
		types.AttrPrefix + "used_percent":  fmt.Sprintf("%.1f%%", vm.UsedPercent),
		types.AttrPrefix + "buffers":       conv.HumanBytes(vm.Buffers),
		types.AttrPrefix + "cached":        conv.HumanBytes(vm.Cached),
	}).SetTitleRule(tr).SetDescription("everything is ok")

	status := types.EvaluateGeThreshold(vm.UsedPercent, ins.MemoryUsage.WarnGe, ins.MemoryUsage.CriticalGe)
	if status != types.EventStatusOk {
		threshold := ins.MemoryUsage.WarnGe
		level := "warning"
		if status == types.EventStatusCritical {
			threshold = ins.MemoryUsage.CriticalGe
			level = "critical"
		}
		q.PushFront(event.SetEventStatus(status).
			SetDescription(fmt.Sprintf("memory usage %.1f%% >= %s threshold %.1f%%, total: %s, available: %s",
				vm.UsedPercent, level, threshold, conv.HumanBytes(vm.Total), conv.HumanBytes(vm.Available))))
		return
	}

	q.PushFront(event)
}

func (ins *Instance) checkSwapUsage(q *safe.Queue[*types.Event]) {
	if ins.SwapUsage.WarnGe == 0 && ins.SwapUsage.CriticalGe == 0 {
		return
	}

	swap, err := mem.SwapMemory()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "mem::swap_usage",
			"target": "memory",
		}).SetTitleRule("[check]").
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to get swap info: %v", err)))
		return
	}

	// Swap 未启用时静默跳过
	if swap.Total == 0 {
		return
	}

	tr := ins.SwapUsage.TitleRule
	if tr == "" {
		tr = "[check]"
	}

	event := types.BuildEvent(map[string]string{
		"check":                                 "mem::swap_usage",
		"target":                                "memory",
		types.AttrPrefix + "swap_total":         conv.HumanBytes(swap.Total),
		types.AttrPrefix + "swap_used":          conv.HumanBytes(swap.Used),
		types.AttrPrefix + "swap_free":          conv.HumanBytes(swap.Free),
		types.AttrPrefix + "swap_used_percent":  fmt.Sprintf("%.1f%%", swap.UsedPercent),
	}).SetTitleRule(tr).SetDescription("everything is ok")

	status := types.EvaluateGeThreshold(swap.UsedPercent, ins.SwapUsage.WarnGe, ins.SwapUsage.CriticalGe)
	if status != types.EventStatusOk {
		threshold := ins.SwapUsage.WarnGe
		level := "warning"
		if status == types.EventStatusCritical {
			threshold = ins.SwapUsage.CriticalGe
			level = "critical"
		}
		q.PushFront(event.SetEventStatus(status).
			SetDescription(fmt.Sprintf("swap usage %.1f%% >= %s threshold %.1f%%, total: %s, free: %s",
				swap.UsedPercent, level, threshold, conv.HumanBytes(swap.Total), conv.HumanBytes(swap.Free))))
		return
	}

	q.PushFront(event)
}
