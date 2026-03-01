package tcpstate

import (
	"fmt"
	"runtime"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "tcpstate"

type StateCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type stateCounts struct {
	established uint64
	closeWait   uint64
	timeWait    uint64
}

type Instance struct {
	config.InternalConfig

	CloseWait StateCheck `toml:"close_wait"`
	TimeWait  StateCheck `toml:"time_wait"`

	hasCloseWaitCheck bool
	hasTimeWaitCheck  bool
}

type TcpstatePlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

var (
	runtimeGOOS    = runtime.GOOS
	queryStatesFn  func() (*stateCounts, error)
	readTimeWaitFn func() (uint64, error)
)

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &TcpstatePlugin{}
	})
}

func (p *TcpstatePlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func (ins *Instance) Init() error {
	if runtimeGOOS != "linux" {
		return fmt.Errorf("tcpstate plugin only supports linux (current: %s)", runtimeGOOS)
	}

	ins.hasCloseWaitCheck = ins.CloseWait.WarnGe > 0 || ins.CloseWait.CriticalGe > 0
	ins.hasTimeWaitCheck = ins.TimeWait.WarnGe > 0 || ins.TimeWait.CriticalGe > 0

	if !ins.hasCloseWaitCheck && !ins.hasTimeWaitCheck {
		return fmt.Errorf("at least one check must be configured (close_wait or time_wait)")
	}

	if err := validateThresholds("close_wait", ins.CloseWait); err != nil {
		return err
	}
	if err := validateThresholds("time_wait", ins.TimeWait); err != nil {
		return err
	}

	return nil
}

func validateThresholds(name string, sc StateCheck) error {
	if sc.WarnGe < 0 {
		return fmt.Errorf("%s.warn_ge must be non-negative (got %g)", name, sc.WarnGe)
	}
	if sc.CriticalGe < 0 {
		return fmt.Errorf("%s.critical_ge must be non-negative (got %g)", name, sc.CriticalGe)
	}
	if sc.WarnGe > 0 && sc.CriticalGe > 0 && sc.WarnGe >= sc.CriticalGe {
		return fmt.Errorf("%s.warn_ge(%g) must be less than %s.critical_ge(%g)",
			name, sc.WarnGe, name, sc.CriticalGe)
	}
	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.hasCloseWaitCheck {
		ins.gatherViaNetlink(q)
	} else if ins.hasTimeWaitCheck {
		ins.gatherViaSockstat(q)
	}
}

func (ins *Instance) gatherViaNetlink(q *safe.Queue[*types.Event]) {
	counts, err := queryStatesFn()
	if err != nil {
		q.PushFront(ins.buildErrorEvent(
			fmt.Sprintf("failed to query TCP states via netlink: %v", err)))
		return
	}

	ins.emitStateEvent(q, "tcpstate::close_wait", "CLOSE_WAIT",
		counts.closeWait, ins.CloseWait, int64(counts.established))

	if ins.hasTimeWaitCheck {
		ins.emitStateEvent(q, "tcpstate::time_wait", "TIME_WAIT",
			counts.timeWait, ins.TimeWait, int64(counts.established))
	}
}

func (ins *Instance) gatherViaSockstat(q *safe.Queue[*types.Event]) {
	twCount, err := readTimeWaitFn()
	if err != nil {
		q.PushFront(ins.buildErrorEvent(
			fmt.Sprintf("failed to read sockstat: %v", err)))
		return
	}

	ins.emitStateEvent(q, "tcpstate::time_wait", "TIME_WAIT",
		twCount, ins.TimeWait, -1)
}

func (ins *Instance) emitStateEvent(q *safe.Queue[*types.Event], check, stateName string,
	count uint64, sc StateCheck, establishedCount int64) {

	tr := sc.TitleRule
	if tr == "" {
		tr = "[check]"
	}

	labels := map[string]string{
		"check":                    check,
		"target":                   "system",
		types.AttrPrefix + "count": fmt.Sprintf("%d", count),
	}
	if establishedCount >= 0 {
		labels[types.AttrPrefix+"established"] = fmt.Sprintf("%d", establishedCount)
	}

	event := types.BuildEvent(labels).SetTitleRule(tr)
	fcount := float64(count)

	status := types.EvaluateGeThreshold(fcount, sc.WarnGe, sc.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("%d %s connections, above critical threshold %.0f",
			count, stateName, sc.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("%d %s connections, above warning threshold %.0f",
			count, stateName, sc.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("%d %s connections, everything is ok",
			count, stateName))
	}

	q.PushFront(event)
}

func (ins *Instance) buildErrorEvent(errMsg string) *types.Event {
	check := "tcpstate::close_wait"
	if !ins.hasCloseWaitCheck {
		check = "tcpstate::time_wait"
	}
	return types.BuildEvent(map[string]string{
		"check":  check,
		"target": "system",
	}).SetTitleRule("[check]").
		SetEventStatus(types.EventStatusCritical).
		SetDescription(errMsg)
}
