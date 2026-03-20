package cpu

import (
	"fmt"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/plugins"
	"github.com/cprobe/catpaw/digcore/types"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/load"
)

const pluginName = "cpu"

type CpuUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
}

type LoadAverageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	Period     string  `toml:"period"`
}

type Instance struct {
	config.InternalConfig

	CpuUsage    CpuUsageCheck    `toml:"cpu_usage"`
	LoadAverage LoadAverageCheck `toml:"load_average"`

	cpuCores int
}

type CpuPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *CpuPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &CpuPlugin{}
	})
}

func (ins *Instance) Init() error {
	if ins.CpuUsage.WarnGe < 0 || ins.CpuUsage.WarnGe > 100 ||
		ins.CpuUsage.CriticalGe < 0 || ins.CpuUsage.CriticalGe > 100 {
		return fmt.Errorf("cpu_usage thresholds must be between 0 and 100 (got warn_ge=%.1f, critical_ge=%.1f)",
			ins.CpuUsage.WarnGe, ins.CpuUsage.CriticalGe)
	}

	if ins.CpuUsage.WarnGe > 0 && ins.CpuUsage.CriticalGe > 0 &&
		ins.CpuUsage.WarnGe >= ins.CpuUsage.CriticalGe {
		return fmt.Errorf("cpu_usage.warn_ge(%.1f) must be less than cpu_usage.critical_ge(%.1f)",
			ins.CpuUsage.WarnGe, ins.CpuUsage.CriticalGe)
	}

	if ins.LoadAverage.WarnGe < 0 || ins.LoadAverage.CriticalGe < 0 {
		return fmt.Errorf("load_average thresholds must be >= 0 (got warn_ge=%.2f, critical_ge=%.2f)",
			ins.LoadAverage.WarnGe, ins.LoadAverage.CriticalGe)
	}

	if ins.LoadAverage.WarnGe > 0 && ins.LoadAverage.CriticalGe > 0 &&
		ins.LoadAverage.WarnGe >= ins.LoadAverage.CriticalGe {
		return fmt.Errorf("load_average.warn_ge(%.2f) must be less than load_average.critical_ge(%.2f)",
			ins.LoadAverage.WarnGe, ins.LoadAverage.CriticalGe)
	}

	if ins.LoadAverage.Period == "" {
		ins.LoadAverage.Period = "5m"
	}

	switch ins.LoadAverage.Period {
	case "1m", "5m", "15m":
	default:
		return fmt.Errorf("load_average.period must be one of: 1m, 5m, 15m (got %q)", ins.LoadAverage.Period)
	}

	cores, err := cpu.Counts(true)
	if err != nil {
		return fmt.Errorf("failed to get CPU core count: %v", err)
	}
	if cores <= 0 {
		cores = 1
	}
	ins.cpuCores = cores

	// Warm up gopsutil's internal CPU snapshot so the first Gather
	// gets a meaningful delta instead of a stale-or-zero value.
	cpu.Percent(0, false)

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	ins.checkCpuUsage(q)
	ins.checkLoadAverage(q)
}

func (ins *Instance) checkCpuUsage(q *safe.Queue[*types.Event]) {
	if ins.CpuUsage.WarnGe == 0 && ins.CpuUsage.CriticalGe == 0 {
		return
	}

	percents, err := cpu.Percent(0, false)
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "cpu::cpu_usage",
			"target": "cpu",
		}).SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to get CPU usage: %v", err)))
		return
	}

	if len(percents) == 0 {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "cpu::cpu_usage",
			"target": "cpu",
		}).SetEventStatus(types.EventStatusCritical).
			SetDescription("cpu.Percent returned empty result"))
		return
	}

	usage := percents[0]

	event := types.BuildEvent(map[string]string{
		"check":  "cpu::cpu_usage",
		"target": "cpu",
	}).SetAttrs(map[string]string{
		"cpu_usage": fmt.Sprintf("%.1f%%", usage),
		"cpu_cores": fmt.Sprintf("%d", ins.cpuCores),
	}).SetCurrentValue(fmt.Sprintf("%.1f%%", usage)).SetDescription("everything is ok")
	var parts []string
	if ins.CpuUsage.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning ≥ %.1f%%", ins.CpuUsage.WarnGe))
	}
	if ins.CpuUsage.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical ≥ %.1f%%", ins.CpuUsage.CriticalGe))
	}
	if len(parts) > 0 {
		event.Attrs["threshold_desc"] = strings.Join(parts, ", ")
	}

	intervalHint := "interval avg"
	if ins.Interval > 0 {
		intervalHint = fmt.Sprintf("%s avg", time.Duration(ins.Interval))
	}

	status := types.EvaluateGeThreshold(usage, ins.CpuUsage.WarnGe, ins.CpuUsage.CriticalGe)
	if status != types.EventStatusOk {
		threshold := ins.CpuUsage.WarnGe
		level := "warning"
		if status == types.EventStatusCritical {
			threshold = ins.CpuUsage.CriticalGe
			level = "critical"
		}
		q.PushFront(event.SetEventStatus(status).
			SetDescription(fmt.Sprintf("CPU usage %.1f%% (%s) >= %s threshold %.1f%%, cores: %d",
				usage, intervalHint, level, threshold, ins.cpuCores)))
		return
	}

	q.PushFront(event)
}

func (ins *Instance) checkLoadAverage(q *safe.Queue[*types.Event]) {
	if ins.LoadAverage.WarnGe == 0 && ins.LoadAverage.CriticalGe == 0 {
		return
	}

	avg, err := load.Avg()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "cpu::load_average",
			"target": "cpu",
		}).SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to get load average: %v", err)))
		return
	}

	var rawLoad float64
	switch ins.LoadAverage.Period {
	case "1m":
		rawLoad = avg.Load1
	case "15m":
		rawLoad = avg.Load15
	default:
		rawLoad = avg.Load5
	}

	perCoreLoad := rawLoad / float64(ins.cpuCores)

	event := types.BuildEvent(map[string]string{
		"check":  "cpu::load_average",
		"target": "cpu",
	}).SetAttrs(map[string]string{
		"load1":         fmt.Sprintf("%.2f", avg.Load1),
		"load5":         fmt.Sprintf("%.2f", avg.Load5),
		"load15":        fmt.Sprintf("%.2f", avg.Load15),
		"per_core_load": fmt.Sprintf("%.2f", perCoreLoad),
		"cpu_cores":     fmt.Sprintf("%d", ins.cpuCores),
		"period":        ins.LoadAverage.Period,
	}).SetCurrentValue(fmt.Sprintf("%.2f", perCoreLoad)).SetDescription("everything is ok")
	var loadParts []string
	if ins.LoadAverage.WarnGe > 0 {
		loadParts = append(loadParts, fmt.Sprintf("Warning ≥ %.2f", ins.LoadAverage.WarnGe))
	}
	if ins.LoadAverage.CriticalGe > 0 {
		loadParts = append(loadParts, fmt.Sprintf("Critical ≥ %.2f", ins.LoadAverage.CriticalGe))
	}
	if len(loadParts) > 0 {
		event.Attrs["threshold_desc"] = strings.Join(loadParts, ", ")
	}

	status := types.EvaluateGeThreshold(perCoreLoad, ins.LoadAverage.WarnGe, ins.LoadAverage.CriticalGe)
	if status != types.EventStatusOk {
		threshold := ins.LoadAverage.WarnGe
		level := "warning"
		if status == types.EventStatusCritical {
			threshold = ins.LoadAverage.CriticalGe
			level = "critical"
		}
		q.PushFront(event.SetEventStatus(status).
			SetDescription(fmt.Sprintf("load average (%s) per-core %.2f >= %s threshold %.2f, raw load: %.2f, cores: %d",
				ins.LoadAverage.Period, perCoreLoad, level, threshold, rawLoad, ins.cpuCores)))
		return
	}

	q.PushFront(event)
}
