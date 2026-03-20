package procnum

import (
	"fmt"
	"strings"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/pkg/procutil"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/plugins"
	"github.com/cprobe/catpaw/digcore/types"
	"github.com/shirou/gopsutil/v3/process"
)

const pluginName = "procnum"

type searchMode int

const (
	searchModeProcess searchMode = iota // exec_name / cmdline / user — AND combination
	searchModePidFile                   // standalone: read PID from file
)

type ProcessCountCheck struct {
	WarnLt     *int `toml:"warn_lt"`
	CriticalLt *int `toml:"critical_lt"`
	WarnGt     *int `toml:"warn_gt"`
	CriticalGt *int `toml:"critical_gt"`
}

type Instance struct {
	config.InternalConfig

	SearchExecName string `toml:"search_exec_name"`
	SearchCmdline  string `toml:"search_cmdline"`
	SearchUser     string `toml:"search_user"`

	SearchPidFile string `toml:"search_pid_file"`

	mode        searchMode
	searchLabel string

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
	ins.SearchExecName = strings.TrimSpace(ins.SearchExecName)
	ins.SearchCmdline = strings.TrimSpace(ins.SearchCmdline)
	ins.SearchUser = strings.TrimSpace(ins.SearchUser)
	ins.SearchPidFile = strings.TrimSpace(ins.SearchPidFile)

	hasProcessFilter := ins.SearchExecName != "" || ins.SearchCmdline != "" || ins.SearchUser != ""
	hasPidFile := ins.SearchPidFile != ""

	if hasProcessFilter && hasPidFile {
		return fmt.Errorf("search_pid_file is mutually exclusive with process filters (search_exec_name/search_cmdline/search_user)")
	}

	if hasPidFile {
		ins.mode = searchModePidFile
	} else {
		ins.mode = searchModeProcess
	}

	ins.searchLabel = ins.buildSearchLabel()

	pc := ins.ProcessCount
	if pc.WarnLt == nil && pc.CriticalLt == nil && pc.WarnGt == nil && pc.CriticalGt == nil {
		return fmt.Errorf("at least one process_count threshold must be configured")
	}

	if pc.WarnLt != nil && *pc.WarnLt < 0 {
		return fmt.Errorf("process_count.warn_lt must be non-negative (got %d)", *pc.WarnLt)
	}
	if pc.CriticalLt != nil && *pc.CriticalLt < 0 {
		return fmt.Errorf("process_count.critical_lt must be non-negative (got %d)", *pc.CriticalLt)
	}
	if pc.WarnGt != nil && *pc.WarnGt < 0 {
		return fmt.Errorf("process_count.warn_gt must be non-negative (got %d)", *pc.WarnGt)
	}
	if pc.CriticalGt != nil && *pc.CriticalGt < 0 {
		return fmt.Errorf("process_count.critical_gt must be non-negative (got %d)", *pc.CriticalGt)
	}

	if pc.WarnLt != nil && pc.CriticalLt != nil && *pc.WarnLt < *pc.CriticalLt {
		return fmt.Errorf("process_count.warn_lt(%d) must be >= process_count.critical_lt(%d)", *pc.WarnLt, *pc.CriticalLt)
	}
	if pc.WarnGt != nil && pc.CriticalGt != nil && *pc.WarnGt > *pc.CriticalGt {
		return fmt.Errorf("process_count.warn_gt(%d) must be <= process_count.critical_gt(%d)", *pc.WarnGt, *pc.CriticalGt)
	}

	return nil
}

func (ins *Instance) buildSearchLabel() string {
	if ins.SearchPidFile != "" {
		return ins.SearchPidFile
	}

	var parts []string
	if ins.SearchExecName != "" {
		parts = append(parts, ins.SearchExecName)
	}
	if ins.SearchCmdline != "" {
		parts = append(parts, ins.SearchCmdline)
	}
	if ins.SearchUser != "" {
		parts = append(parts, "user:"+ins.SearchUser)
	}
	if len(parts) == 0 {
		return "all"
	}
	return strings.Join(parts, " && ")
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	var (
		count int
		err   error
	)

	switch ins.mode {
	case searchModePidFile:
		pids, e := ins.findByPidFile()
		count, err = len(pids), e
	case searchModeProcess:
		if ins.searchLabel == "all" {
			count, err = procutil.CountAllProcesses()
		} else {
			pids, e := ins.findProcesses()
			count, err = len(pids), e
		}
	default:
		logger.Logger.Error("procnum: unreachable - unknown search mode")
		return
	}

	if err != nil {
		q.PushFront(ins.newEvent().SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("search error: %v", err)))
		return
	}

	logger.Logger.Debugw("search result", "target", ins.searchLabel, "count", count)

	pc := ins.ProcessCount
	var parts []string
	if pc.WarnLt != nil {
		parts = append(parts, fmt.Sprintf("Warning < %d", *pc.WarnLt))
	}
	if pc.CriticalLt != nil {
		parts = append(parts, fmt.Sprintf("Critical < %d", *pc.CriticalLt))
	}
	if pc.WarnGt != nil {
		parts = append(parts, fmt.Sprintf("Warning > %d", *pc.WarnGt))
	}
	if pc.CriticalGt != nil {
		parts = append(parts, fmt.Sprintf("Critical > %d", *pc.CriticalGt))
	}
	attrs := map[string]string{
		"process_count":  fmt.Sprintf("%d", count),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent().SetAttrs(attrs)

	if pc.CriticalLt != nil && count < *pc.CriticalLt {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("process count %d < critical threshold %d", count, *pc.CriticalLt)))
		return
	}
	if pc.WarnLt != nil && count < *pc.WarnLt {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("process count %d < warning threshold %d", count, *pc.WarnLt)))
		return
	}
	if pc.CriticalGt != nil && count > *pc.CriticalGt {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("process count %d > critical threshold %d", count, *pc.CriticalGt)))
		return
	}
	if pc.WarnGt != nil && count > *pc.WarnGt {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("process count %d > warning threshold %d", count, *pc.WarnGt)))
		return
	}

	event.SetDescription(fmt.Sprintf("process count %d, everything is ok", count))
	q.PushFront(event)
}

// findProcesses enumerates all system processes and returns those matching
// ALL configured conditions (exec_name, cmdline, user) via AND logic.
func (ins *Instance) findProcesses() ([]procutil.PID, error) {
	procs, err := procutil.FastProcessList()
	if err != nil {
		return nil, err
	}

	var pids []procutil.PID
	for _, p := range procs {
		if ins.matchProcess(p) {
			pids = append(pids, procutil.PID(p.Pid))
		}
	}
	return pids, nil
}

// matchProcess returns true only if the process satisfies ALL configured conditions.
func (ins *Instance) matchProcess(p *process.Process) bool {
	if ins.SearchExecName != "" {
		name, err := procutil.ProcessExecName(p)
		if err != nil {
			return false
		}
		if !strings.Contains(name, ins.SearchExecName) {
			return false
		}
	}

	if ins.SearchCmdline != "" {
		cmd, err := p.Cmdline()
		if err != nil {
			return false
		}
		if !strings.Contains(cmd, ins.SearchCmdline) {
			return false
		}
	}

	if ins.SearchUser != "" {
		username, err := p.Username()
		if err != nil {
			return false
		}
		if username != ins.SearchUser {
			return false
		}
	}

	return true
}

// findByPidFile reads a PID from a file and verifies the process is still alive.
func (ins *Instance) findByPidFile() ([]procutil.PID, error) {
	pids, err := procutil.ReadPidFile(ins.SearchPidFile)
	if err != nil {
		return nil, err
	}

	var alive []procutil.PID
	for _, pid := range pids {
		exists, err := process.PidExists(int32(pid))
		if err != nil {
			continue
		}
		if exists {
			alive = append(alive, pid)
		}
	}
	return alive, nil
}

func (ins *Instance) newEvent() *types.Event {
	attrs := map[string]string{}
	if ins.SearchExecName != "" {
		attrs["search_exec_name"] = ins.SearchExecName
	}
	if ins.SearchCmdline != "" {
		attrs["search_cmdline"] = ins.SearchCmdline
	}
	if ins.SearchUser != "" {
		attrs["search_user"] = ins.SearchUser
	}
	if ins.SearchPidFile != "" {
		attrs["search_pid_file"] = ins.SearchPidFile
	}

	return types.BuildEvent(map[string]string{
		"check":  "procnum::process_count",
		"target": ins.searchLabel,
	}).SetAttrs(attrs)
}
