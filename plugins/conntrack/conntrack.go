package conntrack

import (
	"errors"
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

const pluginName = "conntrack"

var errModuleNotLoaded = errors.New("nf_conntrack module not loaded")

var conntrackPaths = [][2]string{
	{"/proc/sys/net/netfilter/nf_conntrack_count", "/proc/sys/net/netfilter/nf_conntrack_max"},
	{"/proc/sys/net/ipv4/netfilter/ip_conntrack_count", "/proc/sys/net/ipv4/netfilter/ip_conntrack_max"},
}

type ConntrackUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	ConntrackUsage ConntrackUsageCheck `toml:"conntrack_usage"`
}

type ConntrackPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *ConntrackPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &ConntrackPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("conntrack plugin only supports linux (current: %s)", runtime.GOOS)
	}

	if ins.ConntrackUsage.WarnGe < 0 || ins.ConntrackUsage.WarnGe > 100 ||
		ins.ConntrackUsage.CriticalGe < 0 || ins.ConntrackUsage.CriticalGe > 100 {
		return fmt.Errorf("conntrack_usage thresholds must be between 0 and 100 (got warn_ge=%.1f, critical_ge=%.1f)",
			ins.ConntrackUsage.WarnGe, ins.ConntrackUsage.CriticalGe)
	}

	if ins.ConntrackUsage.WarnGe > 0 && ins.ConntrackUsage.CriticalGe > 0 && ins.ConntrackUsage.WarnGe >= ins.ConntrackUsage.CriticalGe {
		return fmt.Errorf("conntrack_usage.warn_ge(%.1f) must be less than conntrack_usage.critical_ge(%.1f)",
			ins.ConntrackUsage.WarnGe, ins.ConntrackUsage.CriticalGe)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.ConntrackUsage.WarnGe == 0 && ins.ConntrackUsage.CriticalGe == 0 {
		return
	}

	tr := ins.ConntrackUsage.TitleRule
	if tr == "" {
		tr = "[check]"
	}

	count, max, err := readConntrackFiles()
	if errors.Is(err, errModuleNotLoaded) {
		return
	}
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "conntrack::conntrack_usage",
			"target": "system",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read conntrack data: %v", err)))
		return
	}

	if max == 0 {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "conntrack::conntrack_usage",
			"target": "system",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription("nf_conntrack_max is 0, cannot calculate usage"))
		return
	}

	usagePercent := float64(count) / float64(max) * 100
	countStr := strconv.FormatUint(count, 10)
	maxStr := strconv.FormatUint(max, 10)

	event := types.BuildEvent(map[string]string{
		"check":                            "conntrack::conntrack_usage",
		"target":                           "system",
		types.AttrPrefix + "count":         countStr,
		types.AttrPrefix + "max":           maxStr,
		types.AttrPrefix + "usage_percent": fmt.Sprintf("%.1f%%", usagePercent),
	}).SetTitleRule(tr)

	status := types.EvaluateGeThreshold(usagePercent, ins.ConntrackUsage.WarnGe, ins.ConntrackUsage.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("conntrack usage %.1f%% (%s/%s), above critical threshold %.0f%%",
			usagePercent, countStr, maxStr, ins.ConntrackUsage.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("conntrack usage %.1f%% (%s/%s), above warning threshold %.0f%%",
			usagePercent, countStr, maxStr, ins.ConntrackUsage.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("conntrack usage %.1f%% (%s/%s), everything is ok",
			usagePercent, countStr, maxStr))
	}

	q.PushFront(event)
}

func readConntrackFiles() (count, max uint64, err error) {
	for _, paths := range conntrackPaths {
		countPath, maxPath := paths[0], paths[1]

		countBytes, err1 := os.ReadFile(countPath)
		maxBytes, err2 := os.ReadFile(maxPath)

		if err1 == nil && err2 == nil {
			count, err = strconv.ParseUint(strings.TrimSpace(string(countBytes)), 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse %s: %v", countPath, err)
			}
			max, err = strconv.ParseUint(strings.TrimSpace(string(maxBytes)), 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse %s: %v", maxPath, err)
			}
			return count, max, nil
		}

		if !os.IsNotExist(err1) && err1 != nil {
			return 0, 0, fmt.Errorf("read %s: %v", countPath, err1)
		}
		if !os.IsNotExist(err2) && err2 != nil {
			return 0, 0, fmt.Errorf("read %s: %v", maxPath, err2)
		}
	}

	return 0, 0, errModuleNotLoaded
}
