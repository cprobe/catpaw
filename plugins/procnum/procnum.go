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

type Instance struct {
	config.InternalConfig

	SearchExecSubstring    string `toml:"search_exec_substring"`
	SearchCmdlineSubstring string `toml:"search_cmdline_substring"`
	SearchWinService       string `toml:"search_win_service"`

	AlertIfNumLt int    `toml:"alert_if_num_lt"`
	Check        string `toml:"check"`
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

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.SearchExecSubstring == "" && ins.SearchCmdlineSubstring == "" && ins.SearchWinService == "" {
		return
	}

	if ins.Check == "" {
		logger.Logger.Error("check is empty")
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
		q.PushFront(ins.buildEvent("Occur error: " + err.Error()).SetEventStatus(ins.GetDefaultSeverity()))
		return
	}

	if len(pids) < ins.AlertIfNumLt {
		s := fmt.Sprintf("The number of process is less than expected. real: %d, expected: %d", len(pids), ins.AlertIfNumLt)
		q.PushFront(ins.buildEvent(s).SetEventStatus(ins.GetDefaultSeverity()))
		return
	}

	q.PushFront(ins.buildEvent())
}

func (ins *Instance) buildEvent(desc ...string) *types.Event {
	event := types.BuildEvent(map[string]string{"check": ins.Check}).SetTitleRule("$check")
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
