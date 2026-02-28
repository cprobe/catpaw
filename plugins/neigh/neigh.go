package neigh

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "neigh"

var (
	arpPath      = "/proc/net/arp"
	gcThresh3Path = "/proc/sys/net/ipv4/neigh/default/gc_thresh3"
)

type NeighUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	NeighUsage NeighUsageCheck `toml:"neigh_usage"`
}

type NeighPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *NeighPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &NeighPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("neigh plugin only supports linux (current: %s)", runtime.GOOS)
	}

	if ins.NeighUsage.WarnGe < 0 || ins.NeighUsage.WarnGe > 100 ||
		ins.NeighUsage.CriticalGe < 0 || ins.NeighUsage.CriticalGe > 100 {
		return fmt.Errorf("neigh_usage thresholds must be between 0 and 100 (got warn_ge=%.1f, critical_ge=%.1f)",
			ins.NeighUsage.WarnGe, ins.NeighUsage.CriticalGe)
	}

	if ins.NeighUsage.WarnGe > 0 && ins.NeighUsage.CriticalGe > 0 && ins.NeighUsage.WarnGe >= ins.NeighUsage.CriticalGe {
		return fmt.Errorf("neigh_usage.warn_ge(%.1f) must be less than neigh_usage.critical_ge(%.1f)",
			ins.NeighUsage.WarnGe, ins.NeighUsage.CriticalGe)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.NeighUsage.WarnGe == 0 && ins.NeighUsage.CriticalGe == 0 {
		return
	}

	tr := ins.NeighUsage.TitleRule
	if tr == "" {
		tr = "[check]"
	}

	entries, gcThresh3, err := readNeighData()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "neigh::neigh_usage",
			"target": "system",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read neigh data: %v", err)))
		return
	}

	if gcThresh3 == 0 {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "neigh::neigh_usage",
			"target": "system",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription("gc_thresh3 is 0, cannot calculate usage"))
		return
	}

	usagePercent := float64(entries) / float64(gcThresh3) * 100
	entriesStr := strconv.FormatUint(entries, 10)
	gcThresh3Str := strconv.FormatUint(gcThresh3, 10)

	event := types.BuildEvent(map[string]string{
		"check":                                "neigh::neigh_usage",
		"target":                               "system",
		types.AttrPrefix + "entries":           entriesStr,
		types.AttrPrefix + "gc_thresh3":        gcThresh3Str,
		types.AttrPrefix + "usage_percent":     fmt.Sprintf("%.1f%%", usagePercent),
	}).SetTitleRule(tr)

	status := types.EvaluateGeThreshold(usagePercent, ins.NeighUsage.WarnGe, ins.NeighUsage.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("neigh table usage %.1f%% (%s/%s), above critical threshold %.0f%%",
			usagePercent, entriesStr, gcThresh3Str, ins.NeighUsage.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("neigh table usage %.1f%% (%s/%s), above warning threshold %.0f%%",
			usagePercent, entriesStr, gcThresh3Str, ins.NeighUsage.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("neigh table usage %.1f%% (%s/%s), everything is ok",
			usagePercent, entriesStr, gcThresh3Str))
	}

	q.PushFront(event)
}

func readNeighData() (entries, gcThresh3 uint64, err error) {
	thresh3Data, err := os.ReadFile(gcThresh3Path)
	if err != nil {
		return 0, 0, fmt.Errorf("read gc_thresh3: %v", err)
	}

	gcThresh3, err = strconv.ParseUint(strings.TrimSpace(string(thresh3Data)), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse gc_thresh3: %v", err)
	}

	arpData, err := os.ReadFile(arpPath)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %v", arpPath, err)
	}

	content := strings.TrimSpace(string(arpData))
	if content == "" {
		return 0, gcThresh3, nil
	}

	lines := strings.Split(content, "\n")
	if len(lines) <= 1 {
		return 0, gcThresh3, nil
	}

	entries = uint64(len(lines) - 1)
	return entries, gcThresh3, nil
}
