package zombie

import (
	"fmt"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/procutil"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "zombie"

type Instance struct {
	config.InternalConfig

	WarnGt     *int   `toml:"warn_gt"`
	CriticalGt *int   `toml:"critical_gt"`
	TitleRule  string `toml:"title_rule"`
}

type ZombiePlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *ZombiePlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &ZombiePlugin{}
	})
}

func (ins *Instance) Init() error {
	if ins.WarnGt == nil && ins.CriticalGt == nil {
		return fmt.Errorf("at least one threshold must be configured (warn_gt or critical_gt)")
	}

	if ins.WarnGt != nil && *ins.WarnGt < 0 {
		return fmt.Errorf("warn_gt must be non-negative (got %d)", *ins.WarnGt)
	}
	if ins.CriticalGt != nil && *ins.CriticalGt < 0 {
		return fmt.Errorf("critical_gt must be non-negative (got %d)", *ins.CriticalGt)
	}
	if ins.WarnGt != nil && ins.CriticalGt != nil && *ins.WarnGt > *ins.CriticalGt {
		return fmt.Errorf("warn_gt(%d) must be <= critical_gt(%d)", *ins.WarnGt, *ins.CriticalGt)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	count, err := countZombieProcesses()
	if err != nil {
		q.PushFront(ins.newEvent().
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to count zombie processes: %v", err)))
		return
	}

	logger.Logger.Debugw("zombie count", "count", count)

	event := ins.newEvent()
	event.Labels[types.AttrPrefix+"zombie_count"] = fmt.Sprintf("%d", count)
	if ins.CriticalGt != nil {
		event.Labels[types.AttrPrefix+"critical_gt"] = fmt.Sprintf("%d", *ins.CriticalGt)
	}
	if ins.WarnGt != nil {
		event.Labels[types.AttrPrefix+"warn_gt"] = fmt.Sprintf("%d", *ins.WarnGt)
	}

	if ins.CriticalGt != nil && count > *ins.CriticalGt {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("zombie process count %d > critical threshold %d", count, *ins.CriticalGt)))
		return
	}
	if ins.WarnGt != nil && count > *ins.WarnGt {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("zombie process count %d > warning threshold %d", count, *ins.WarnGt)))
		return
	}

	event.SetDescription(fmt.Sprintf("zombie process count %d, everything is ok", count))
	q.PushFront(event)
}

func (ins *Instance) newEvent() *types.Event {
	tr := ins.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}
	return types.BuildEvent(map[string]string{
		"check":  "zombie::count",
		"target": "system",
	}).SetTitleRule(tr)
}

func countZombieProcesses() (int, error) {
	procs, err := procutil.FastProcessList()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, p := range procs {
		statuses, err := p.Status()
		if err != nil {
			continue
		}
		for _, s := range statuses {
			if s == "Z" {
				count++
				break
			}
		}
	}
	return count, nil
}
