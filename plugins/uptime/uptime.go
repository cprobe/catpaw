package uptime

import (
	"fmt"
	"strconv"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/shirou/gopsutil/v3/host"
)

const pluginName = "uptime"

type RebootDetectedCheck struct {
	WarnLt     config.Duration `toml:"warn_lt"`
	CriticalLt config.Duration `toml:"critical_lt"`
	TitleRule  string          `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	RebootDetected RebootDetectedCheck `toml:"reboot_detected"`
}

type UptimePlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *UptimePlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &UptimePlugin{}
	})
}

func (ins *Instance) Init() error {
	warnDur := time.Duration(ins.RebootDetected.WarnLt)
	criticalDur := time.Duration(ins.RebootDetected.CriticalLt)

	if warnDur > 0 && criticalDur > 0 && warnDur <= criticalDur {
		return fmt.Errorf("reboot_detected.warn_lt(%s) must be greater than reboot_detected.critical_lt(%s)",
			humanDuration(warnDur), humanDuration(criticalDur))
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	criticalDur := time.Duration(ins.RebootDetected.CriticalLt)
	warnDur := time.Duration(ins.RebootDetected.WarnLt)

	if criticalDur == 0 && warnDur == 0 {
		return
	}

	tr := ins.RebootDetected.TitleRule
	if tr == "" {
		tr = "[check]"
	}

	uptimeSec, err := host.Uptime()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "uptime::reboot_detected",
			"target": "system",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to get system uptime: %v", err)))
		return
	}

	bootTimeSec, _ := host.BootTime()

	uptimeDur := time.Duration(uptimeSec) * time.Second
	uptimeHuman := humanDuration(uptimeDur)

	labels := map[string]string{
		"check":                              "uptime::reboot_detected",
		"target":                             "system",
		types.AttrPrefix + "uptime":          uptimeHuman,
		types.AttrPrefix + "uptime_seconds":  strconv.FormatUint(uptimeSec, 10),
	}

	if bootTimeSec > 0 {
		labels[types.AttrPrefix+"boot_time"] = time.Unix(int64(bootTimeSec), 0).Format("2006-01-02 15:04:05 MST")
	}

	if criticalDur > 0 {
		labels[types.AttrPrefix+"critical_lt"] = humanDuration(criticalDur)
	}
	if warnDur > 0 {
		labels[types.AttrPrefix+"warn_lt"] = humanDuration(warnDur)
	}

	event := types.BuildEvent(labels).SetTitleRule(tr)

	if criticalDur > 0 && uptimeDur < criticalDur {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("system uptime %s, rebooted within critical threshold %s",
				uptimeHuman, humanDuration(criticalDur))))
		return
	}

	if warnDur > 0 && uptimeDur < warnDur {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("system uptime %s, rebooted within warning threshold %s",
				uptimeHuman, humanDuration(warnDur))))
		return
	}

	q.PushFront(event.SetDescription(fmt.Sprintf("system uptime %s, everything is ok", uptimeHuman)))
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}

	totalSec := int(d.Seconds())
	days := totalSec / 86400
	hours := (totalSec % 86400) / 3600
	minutes := (totalSec % 3600) / 60
	seconds := totalSec % 60

	if days > 0 {
		s := fmt.Sprintf("%dd", days)
		if hours > 0 {
			s += fmt.Sprintf(" %dh", hours)
		}
		if minutes > 0 {
			s += fmt.Sprintf(" %dm", minutes)
		}
		return s
	}
	if hours > 0 {
		s := fmt.Sprintf("%dh", hours)
		if minutes > 0 {
			s += fmt.Sprintf(" %dm", minutes)
		}
		if seconds > 0 {
			s += fmt.Sprintf(" %ds", seconds)
		}
		return s
	}
	if minutes > 0 {
		s := fmt.Sprintf("%dm", minutes)
		if seconds > 0 {
			s += fmt.Sprintf(" %ds", seconds)
		}
		return s
	}
	return fmt.Sprintf("%ds", seconds)
}
