package filefd

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

const pluginName = "filefd"

var fileNrPath = "/proc/sys/fs/file-nr"

type FilefdUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	FilefdUsage FilefdUsageCheck `toml:"filefd_usage"`
}

type FilefdPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *FilefdPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &FilefdPlugin{}
	})
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("filefd plugin only supports linux (current: %s)", runtime.GOOS)
	}

	if ins.FilefdUsage.WarnGe < 0 || ins.FilefdUsage.WarnGe > 100 ||
		ins.FilefdUsage.CriticalGe < 0 || ins.FilefdUsage.CriticalGe > 100 {
		return fmt.Errorf("filefd_usage thresholds must be between 0 and 100 (got warn_ge=%.1f, critical_ge=%.1f)",
			ins.FilefdUsage.WarnGe, ins.FilefdUsage.CriticalGe)
	}

	if ins.FilefdUsage.WarnGe > 0 && ins.FilefdUsage.CriticalGe > 0 && ins.FilefdUsage.WarnGe >= ins.FilefdUsage.CriticalGe {
		return fmt.Errorf("filefd_usage.warn_ge(%.1f) must be less than filefd_usage.critical_ge(%.1f)",
			ins.FilefdUsage.WarnGe, ins.FilefdUsage.CriticalGe)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.FilefdUsage.WarnGe == 0 && ins.FilefdUsage.CriticalGe == 0 {
		return
	}

	tr := ins.FilefdUsage.TitleRule
	if tr == "" {
		tr = "[check]"
	}

	allocated, max, err := readFileNr()
	if err != nil {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "filefd::filefd_usage",
			"target": "system",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read file-nr: %v", err)))
		return
	}

	if max == 0 {
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "filefd::filefd_usage",
			"target": "system",
		}).SetTitleRule(tr).
			SetEventStatus(types.EventStatusCritical).
			SetDescription("file-max is 0, cannot calculate usage"))
		return
	}

	usagePercent := float64(allocated) / float64(max) * 100
	allocatedStr := strconv.FormatUint(allocated, 10)
	maxStr := strconv.FormatUint(max, 10)

	event := types.BuildEvent(map[string]string{
		"check":                                "filefd::filefd_usage",
		"target":                               "system",
		types.AttrPrefix + "allocated":         allocatedStr,
		types.AttrPrefix + "max":               maxStr,
		types.AttrPrefix + "usage_percent":     fmt.Sprintf("%.1f%%", usagePercent),
	}).SetTitleRule(tr)

	status := types.EvaluateGeThreshold(usagePercent, ins.FilefdUsage.WarnGe, ins.FilefdUsage.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("filefd usage %.1f%% (%s/%s), above critical threshold %.0f%%",
			usagePercent, allocatedStr, maxStr, ins.FilefdUsage.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("filefd usage %.1f%% (%s/%s), above warning threshold %.0f%%",
			usagePercent, allocatedStr, maxStr, ins.FilefdUsage.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("filefd usage %.1f%% (%s/%s), everything is ok",
			usagePercent, allocatedStr, maxStr))
	}

	q.PushFront(event)
}

func readFileNr() (allocated, max uint64, err error) {
	data, err := os.ReadFile(fileNrPath)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %v", fileNrPath, err)
	}

	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) < 3 {
		return 0, 0, fmt.Errorf("unexpected file-nr format: %q", strings.TrimSpace(string(data)))
	}

	allocated, err = strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse allocated: %v", err)
	}

	max, err = strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse max: %v", err)
	}

	return allocated, max, nil
}
