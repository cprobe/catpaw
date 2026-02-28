package sockstat

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

const pluginName = "sockstat"

var netstatPath = "/proc/net/netstat"

type ListenOverflowCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	ListenOverflow ListenOverflowCheck `toml:"listen_overflow"`

	prevOverflows uint64
	prevDrops     uint64
	initialized   bool
}

type SockstatPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *SockstatPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &SockstatPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("sockstat plugin only supports linux (current: %s)", runtime.GOOS)
	}

	if ins.ListenOverflow.WarnGe < 0 || ins.ListenOverflow.CriticalGe < 0 {
		return fmt.Errorf("listen_overflow thresholds must be >= 0 (got warn_ge=%.1f, critical_ge=%.1f)",
			ins.ListenOverflow.WarnGe, ins.ListenOverflow.CriticalGe)
	}

	if ins.ListenOverflow.WarnGe > 0 && ins.ListenOverflow.CriticalGe > 0 && ins.ListenOverflow.WarnGe >= ins.ListenOverflow.CriticalGe {
		return fmt.Errorf("listen_overflow.warn_ge(%.1f) must be less than listen_overflow.critical_ge(%.1f)",
			ins.ListenOverflow.WarnGe, ins.ListenOverflow.CriticalGe)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.ListenOverflow.WarnGe == 0 && ins.ListenOverflow.CriticalGe == 0 {
		return
	}

	tr := ins.ListenOverflow.TitleRule
	if tr == "" {
		tr = "[check]"
	}

	overflows, drops, err := readListenStats()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "sockstat::listen_overflow",
			"target": "system",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read netstat data: %v", err)))
		return
	}

	overflowsStr := strconv.FormatUint(overflows, 10)
	dropsStr := strconv.FormatUint(drops, 10)

	if !ins.initialized {
		ins.prevOverflows = overflows
		ins.prevDrops = drops
		ins.initialized = true

		q.PushFront(types.BuildEvent(map[string]string{
			"check":                                    "sockstat::listen_overflow",
			"target":                                   "system",
			types.AttrPrefix + "delta":                 "0",
			types.AttrPrefix + "total_overflows":       overflowsStr,
			types.AttrPrefix + "total_drops":           dropsStr,
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusOk).
			SetDescription(fmt.Sprintf("listen overflow baseline established (total overflows: %s)", overflowsStr)))
		return
	}

	var delta uint64
	if overflows >= ins.prevOverflows {
		delta = overflows - ins.prevOverflows
	}

	ins.prevOverflows = overflows
	ins.prevDrops = drops

	deltaStr := strconv.FormatUint(delta, 10)

	event := types.BuildEvent(map[string]string{
		"check":                                    "sockstat::listen_overflow",
		"target":                                   "system",
		types.AttrPrefix + "delta":                 deltaStr,
		types.AttrPrefix + "total_overflows":       overflowsStr,
		types.AttrPrefix + "total_drops":           dropsStr,
	}).SetTitleRule(tr)

	status := types.EvaluateGeThreshold(float64(delta), ins.ListenOverflow.WarnGe, ins.ListenOverflow.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("%s new listen overflows since last check (total: %s), above critical threshold %.0f",
			deltaStr, overflowsStr, ins.ListenOverflow.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("%s new listen overflows since last check (total: %s), above warning threshold %.0f",
			deltaStr, overflowsStr, ins.ListenOverflow.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("no new listen overflows (total: %s), everything is ok", overflowsStr))
	}

	q.PushFront(event)
}

func readListenStats() (overflows, drops uint64, err error) {
	data, err := os.ReadFile(netstatPath)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %v", netstatPath, err)
	}

	lines := strings.Split(string(data), "\n")

	var headerLine, valueLine string
	for _, line := range lines {
		if strings.HasPrefix(line, "TcpExt:") {
			if headerLine == "" {
				headerLine = line
			} else {
				valueLine = line
				break
			}
		}
	}

	if headerLine == "" || valueLine == "" {
		return 0, 0, fmt.Errorf("TcpExt section not found in %s", netstatPath)
	}

	headers := strings.Fields(headerLine)
	values := strings.Fields(valueLine)
	if len(headers) != len(values) {
		return 0, 0, fmt.Errorf("TcpExt header/value count mismatch (%d vs %d)", len(headers), len(values))
	}

	overflowIdx := -1
	dropsIdx := -1
	for i, h := range headers {
		switch h {
		case "ListenOverflows":
			overflowIdx = i
		case "ListenDrops":
			dropsIdx = i
		}
	}

	if overflowIdx < 0 {
		return 0, 0, fmt.Errorf("ListenOverflows not found in TcpExt")
	}

	overflows, err = strconv.ParseUint(values[overflowIdx], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse ListenOverflows: %v", err)
	}

	if dropsIdx >= 0 {
		drops, _ = strconv.ParseUint(values[dropsIdx], 10, 64)
	}

	return overflows, drops, nil
}
