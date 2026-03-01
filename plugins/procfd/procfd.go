package procfd

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/procutil"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/shirou/gopsutil/v3/process"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "procfd"

type FdUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	SearchExecName string `toml:"search_exec_name"`
	SearchCmdline  string `toml:"search_cmdline"`
	SearchUser     string `toml:"search_user"`
	SearchPidFile  string `toml:"search_pid_file"`

	Concurrency int `toml:"concurrency"`

	FdUsage FdUsageCheck `toml:"fd_usage"`

	searchLabel string
}

type ProcfdPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *ProcfdPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &ProcfdPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("procfd plugin only supports linux (current: %s)", runtime.GOOS)
	}

	ins.SearchExecName = strings.TrimSpace(ins.SearchExecName)
	ins.SearchCmdline = strings.TrimSpace(ins.SearchCmdline)
	ins.SearchUser = strings.TrimSpace(ins.SearchUser)
	ins.SearchPidFile = strings.TrimSpace(ins.SearchPidFile)

	hasProcessFilter := ins.SearchExecName != "" || ins.SearchCmdline != "" || ins.SearchUser != ""
	hasPidFile := ins.SearchPidFile != ""

	if !hasProcessFilter && !hasPidFile {
		return fmt.Errorf("at least one search condition must be configured (search_exec_name, search_cmdline, search_user, or search_pid_file)")
	}

	if hasPidFile && hasProcessFilter {
		return fmt.Errorf("search_pid_file is mutually exclusive with other search filters (search_exec_name/search_cmdline/search_user)")
	}

	if ins.Concurrency == 0 {
		ins.Concurrency = 10
	}

	ins.searchLabel = ins.buildSearchLabel()

	if ins.FdUsage.WarnGe == 0 && ins.FdUsage.CriticalGe == 0 {
		return fmt.Errorf("fd_usage thresholds must be configured (warn_ge and/or critical_ge)")
	}

	if ins.FdUsage.WarnGe < 0 || ins.FdUsage.WarnGe > 100 ||
		ins.FdUsage.CriticalGe < 0 || ins.FdUsage.CriticalGe > 100 {
		return fmt.Errorf("fd_usage thresholds must be between 0 and 100 (got warn_ge=%.1f, critical_ge=%.1f)",
			ins.FdUsage.WarnGe, ins.FdUsage.CriticalGe)
	}

	if ins.FdUsage.WarnGe > 0 && ins.FdUsage.CriticalGe > 0 && ins.FdUsage.WarnGe >= ins.FdUsage.CriticalGe {
		return fmt.Errorf("fd_usage.warn_ge(%.1f) must be less than fd_usage.critical_ge(%.1f)",
			ins.FdUsage.WarnGe, ins.FdUsage.CriticalGe)
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
	return strings.Join(parts, " && ")
}

type fdResult struct {
	pid          int32
	openFds      int
	softLimit    uint64
	hardLimit    uint64
	usagePercent float64
	execName     string
	unlimited    bool
	gone         bool
	err          error
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	pids, err := ins.findMatchingProcesses()
	if err != nil {
		q.PushFront(ins.newEvent().
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("search error: %v", err)))
		return
	}

	if len(pids) == 0 {
		logger.Logger.Debugw("procfd: no matching processes found", "target", ins.searchLabel)
		return
	}

	results := ins.collectFdData(pids)

	var worst *fdResult
	matchedCount := len(pids)
	checkedCount := 0
	errorCount := 0

	for i := range results {
		r := &results[i]
		if r.gone {
			continue
		}
		if r.err != nil {
			errorCount++
			continue
		}
		if r.unlimited {
			continue
		}
		checkedCount++
		if worst == nil || r.usagePercent > worst.usagePercent {
			worst = r
		}
	}

	if checkedCount == 0 && errorCount > 0 {
		q.PushFront(ins.newEvent().
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to check fd usage: all %d matched processes returned errors", matchedCount)))
		return
	}

	if checkedCount == 0 {
		logger.Logger.Debugw("procfd: all matched processes have unlimited nofile or exited", "target", ins.searchLabel)
		return
	}

	tr := ins.FdUsage.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":  "procfd::fd_usage",
		"target": ins.searchLabel,
		types.AttrPrefix + "pid":           fmt.Sprintf("%d", worst.pid),
		types.AttrPrefix + "open_fds":      fmt.Sprintf("%d", worst.openFds),
		types.AttrPrefix + "nofile_soft":   fmt.Sprintf("%d", worst.softLimit),
		types.AttrPrefix + "nofile_hard":   fmt.Sprintf("%d", worst.hardLimit),
		types.AttrPrefix + "usage_percent": fmt.Sprintf("%.1f%%", worst.usagePercent),
		types.AttrPrefix + "matched_count": fmt.Sprintf("%d", matchedCount),
		types.AttrPrefix + "checked_count": fmt.Sprintf("%d", checkedCount),
	}).SetTitleRule(tr)

	if worst.execName != "" {
		event.Labels[types.AttrPrefix+"exec_name"] = worst.execName
	}

	status := types.EvaluateGeThreshold(worst.usagePercent, ins.FdUsage.WarnGe, ins.FdUsage.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("fd usage %.1f%% (%d/%d) for pid %d, above critical threshold %.0f%%",
			worst.usagePercent, worst.openFds, worst.softLimit, worst.pid, ins.FdUsage.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("fd usage %.1f%% (%d/%d) for pid %d, above warning threshold %.0f%%",
			worst.usagePercent, worst.openFds, worst.softLimit, worst.pid, ins.FdUsage.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("fd usage %.1f%% (%d/%d), everything is ok",
			worst.usagePercent, worst.openFds, worst.softLimit))
	}

	q.PushFront(event)
}

func (ins *Instance) collectFdData(pids []int32) []fdResult {
	results := make([]fdResult, len(pids))
	var wg sync.WaitGroup
	se := semaphore.NewSemaphore(ins.Concurrency)

	for i, pid := range pids {
		wg.Add(1)
		go func(idx int, pid int32) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Logger.Errorw("panic in procfd collect goroutine", "pid", pid, "recover", r)
					results[idx] = fdResult{pid: pid, err: fmt.Errorf("panic: %v", r)}
				}
			}()
			se.Acquire()
			defer se.Release()
			results[idx] = ins.readProcessFd(pid)
		}(i, pid)
	}
	wg.Wait()
	return results
}

func (ins *Instance) readProcessFd(pid int32) fdResult {
	r := fdResult{pid: pid}

	p := &process.Process{Pid: pid}
	name, _ := procutil.ProcessExecName(p)
	r.execName = name

	soft, hard, err := readNofileLimit(pid)
	if err != nil {
		if procutil.IsProcessGone(err) {
			r.gone = true
			return r
		}
		r.err = fmt.Errorf("read limits: %v", err)
		return r
	}

	if soft == 0 {
		r.unlimited = true
		r.hardLimit = hard
		return r
	}

	openFds, err := countOpenFds(pid)
	if err != nil {
		if procutil.IsProcessGone(err) {
			r.gone = true
			return r
		}
		r.err = fmt.Errorf("count fds: %v", err)
		return r
	}

	r.openFds = openFds
	r.softLimit = soft
	r.hardLimit = hard
	r.usagePercent = float64(openFds) / float64(soft) * 100

	return r
}

func readNofileLimit(pid int32) (soft, hard uint64, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/limits", pid))
	if err != nil {
		return 0, 0, err
	}
	return parseNofileLimit(string(data), pid)
}

// parseNofileLimit extracts the nofile soft/hard limits from /proc/{pid}/limits content.
// soft=0 means unlimited.
func parseNofileLimit(data string, pid int32) (soft, hard uint64, err error) {
	for _, line := range strings.Split(data, "\n") {
		if !strings.HasPrefix(line, "Max open files") {
			continue
		}

		// The label column in /proc/pid/limits is fixed at 26 chars (kernel %-25s + space).
		if len(line) < 27 {
			return 0, 0, fmt.Errorf("unexpected limits line format: %q", line)
		}

		fields := strings.Fields(line[26:])
		if len(fields) < 2 {
			return 0, 0, fmt.Errorf("unexpected limits fields: %q", line)
		}

		if fields[0] == "unlimited" {
			soft = 0
		} else {
			soft, err = strconv.ParseUint(fields[0], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse soft limit: %v", err)
			}
		}

		if fields[1] == "unlimited" {
			hard = 0
		} else {
			hard, err = strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse hard limit: %v", err)
			}
		}

		return soft, hard, nil
	}

	return 0, 0, fmt.Errorf("Max open files not found in /proc/%d/limits", pid)
}

func countOpenFds(pid int32) (int, error) {
	d, err := os.Open(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, err
	}
	defer d.Close()

	names, err := d.Readdirnames(-1)
	if err != nil {
		return 0, err
	}

	return len(names), nil
}

func (ins *Instance) findMatchingProcesses() ([]int32, error) {
	if ins.SearchPidFile != "" {
		return ins.findByPidFile()
	}

	procs, err := procutil.FastProcessList()
	if err != nil {
		return nil, err
	}

	var matched []int32
	for _, p := range procs {
		if ins.matchProcess(p) {
			matched = append(matched, p.Pid)
		}
	}
	return matched, nil
}

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

func (ins *Instance) findByPidFile() ([]int32, error) {
	pids, err := procutil.ReadPidFile(ins.SearchPidFile)
	if err != nil {
		return nil, err
	}

	var alive []int32
	for _, pid := range pids {
		exists, err := process.PidExists(int32(pid))
		if err != nil {
			continue
		}
		if exists {
			alive = append(alive, int32(pid))
		}
	}
	return alive, nil
}

func (ins *Instance) newEvent() *types.Event {
	tr := ins.FdUsage.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}
	return types.BuildEvent(map[string]string{
		"check":  "procfd::fd_usage",
		"target": ins.searchLabel,
	}).SetTitleRule(tr)
}
