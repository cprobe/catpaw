package journaltail

import (
	"bytes"
	"fmt"
	"os/exec"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/filter"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const (
	pluginName string = "journaltail"
)

type Instance struct {
	config.InternalConfig

	TimeSpan      string   `toml:"time_span"`
	Check         string   `toml:"check"`
	FilterInclude []string `toml:"filter_include"`
	FilterExclude []string `toml:"filter_exclude"`

	filter filter.Filter
}

type JournaltailPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *JournaltailPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &JournaltailPlugin{}
	})
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.TimeSpan == "" {
		ins.TimeSpan = "1m"
	}

	if ins.filter == nil {
		if len(ins.FilterInclude) == 0 && len(ins.FilterExclude) == 0 {
			logger.Logger.Error("filter_include and filter_exclude are empty")
			return
		}

		var err error
		ins.filter, err = filter.NewIncludeExcludeFilter(ins.FilterInclude, ins.FilterExclude)
		if err != nil {
			logger.Logger.Warnf("failed to create filter: %s", err)
			return
		}
	}

	if ins.Check == "" {
		logger.Logger.Error("check is empty")
		return
	}

	// go go go
	bin, err := exec.LookPath("journalctl")
	if err != nil {
		logger.Logger.Error("lookup journalctl fail: ", err)
		return
	}

	if bin == "" {
		logger.Logger.Error("journalctl not found")
		return
	}

	out, err := exec.Command(bin, "--since", fmt.Sprintf("-%s", ins.TimeSpan), "--no-pager", "--no-tail").Output()
	if err != nil {
		logger.Logger.Error("exec journalctl fail: ", err)
		return
	}

	var bs bytes.Buffer
	var triggered bool

	bs.WriteString("[MD]")
	bs.WriteString(fmt.Sprintf("- time_span: `%s`\n", ins.TimeSpan))
	bs.WriteString(fmt.Sprintf("- filter_include: `%s`\n", ins.FilterInclude))
	bs.WriteString(fmt.Sprintf("- filter_exclude: `%s`\n", ins.FilterExclude))
	bs.WriteString("\n")
	bs.WriteString("\n")
	bs.WriteString("**matched lines**:\n")
	bs.WriteString("\n```")

	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(line) == 0 {
			continue
		}

		if !ins.filter.Match(string(line)) {
			continue
		}

		triggered = true
		bs.Write(line)
		bs.Write([]byte("\n"))
	}

	bs.WriteString("```")

	e := types.BuildEvent(map[string]string{
		"check": ins.Check,
	}).SetTitleRule("$check")

	if !triggered {
		q.PushFront(e)
		return
	}

	e.SetEventStatus(ins.GetDefaultSeverity())
	e.SetDescription(bs.String())
	q.PushFront(e)
}
