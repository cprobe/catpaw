package procnum

import (
	"fmt"
	"strings"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const (
	pluginName string = "procnum"
)

type ProcessCountCheck struct {
	WarnLt     int    `toml:"warn_lt"`
	CriticalLt int    `toml:"critical_lt"`
	WarnGt     int    `toml:"warn_gt"`
	CriticalGt int    `toml:"critical_gt"`
	TitleRule  string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	SearchExecSubstring    string `toml:"search_exec_substring"`
	SearchCmdlineSubstring string `toml:"search_cmdline_substring"`
	SearchWinService       string `toml:"search_win_service"`

	searchString string

	ProcessCount ProcessCountCheck `toml:"process_count"`
}

type ProcnumPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *ProcnumPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &ProcnumPlugin{}
	})
}

func (ins *Instance) Init() error {
	if ins.SearchExecSubstring != "" {
		ins.searchString = ins.SearchExecSubstring
	} else if ins.SearchCmdlineSubstring != "" {
		ins.searchString = ins.SearchCmdlineSubstring
	} else if ins.SearchWinService != "" {
		ins.searchString = ins.SearchWinService
	}

	pc := ins.ProcessCount
	if pc.WarnLt > 0 && pc.CriticalLt > 0 && pc.WarnLt < pc.CriticalLt {
		return fmt.Errorf("process_count.warn_lt(%d) must be >= process_count.critical_lt(%d)", pc.WarnLt, pc.CriticalLt)
	}
	if pc.WarnGt > 0 && pc.CriticalGt > 0 && pc.WarnGt > pc.CriticalGt {
		return fmt.Errorf("process_count.warn_gt(%d) must be <= process_count.critical_gt(%d)", pc.WarnGt, pc.CriticalGt)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.searchString == "" {
		return
	}

	var (
		pids []PID
		err  error
	)

	pg := NewNativeFinder()
	if ins.SearchExecSubstring != "" {
		pids, err = pg.Pattern(ins.SearchExecSubstring)
	} else if ins.SearchCmdlineSubstring != "" {
		pids, err = pg.FullPattern(ins.SearchCmdlineSubstring)
	} else if ins.SearchWinService != "" {
		pids, err = ins.winServicePIDs()
	} else {
		logger.Logger.Error("Oops... search string not found")
		return
	}

	if err != nil {
		q.PushFront(ins.buildEvent(fmt.Sprintf(`[MD]
- **target**: %s
- **error**: %v
`, ins.searchString, err)).SetEventStatus(types.EventStatusCritical))
		return
	}

	count := len(pids)
	logger.Logger.Debugw("search result", "search_string", ins.searchString, "pids", pids, "count", count)

	pc := ins.ProcessCount
	desc := ins.buildDesc(count)

	// "too few" check: critical_lt has higher priority than warn_lt
	if pc.CriticalLt > 0 && count < pc.CriticalLt {
		q.PushFront(ins.buildEvent(desc).SetEventStatus(types.EventStatusCritical))
		return
	}
	if pc.WarnLt > 0 && count < pc.WarnLt {
		q.PushFront(ins.buildEvent(desc).SetEventStatus(types.EventStatusWarning))
		return
	}

	// "too many" check: critical_gt has higher priority than warn_gt
	if pc.CriticalGt > 0 && count > pc.CriticalGt {
		q.PushFront(ins.buildEvent(desc).SetEventStatus(types.EventStatusCritical))
		return
	}
	if pc.WarnGt > 0 && count > pc.WarnGt {
		q.PushFront(ins.buildEvent(desc).SetEventStatus(types.EventStatusWarning))
		return
	}

	// everything is ok
	q.PushFront(ins.buildEvent(desc))
}

func (ins *Instance) buildDesc(count int) string {
	pc := ins.ProcessCount
	return fmt.Sprintf(`[MD]
- **target**: %s
- **process_count**: %d
- **warn_lt**: %d
- **critical_lt**: %d
- **warn_gt**: %d
- **critical_gt**: %d

**search config**:
- search_exec_substring: %s
- search_cmdline_substring: %s
- search_win_service: %s
`, ins.searchString, count,
		pc.WarnLt, pc.CriticalLt, pc.WarnGt, pc.CriticalGt,
		ins.SearchExecSubstring, ins.SearchCmdlineSubstring, ins.SearchWinService)
}

func (ins *Instance) buildEvent(desc ...string) *types.Event {
	tr := ins.ProcessCount.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":  "procnum::process_count",
		"target": ins.searchString,
	}).SetTitleRule(tr)
	if len(desc) > 0 {
		event.SetDescription(strings.Join(desc, "\n"))
	}
	return event
}

func (ins *Instance) winServicePIDs() ([]PID, error) {
	var pids []PID

	pid, err := queryPidWithWinServiceName(ins.SearchWinService)
	if err != nil {
		return pids, err
	}

	pids = append(pids, PID(pid))

	return pids, nil
}
