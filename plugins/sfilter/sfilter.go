package sfilter

import (
	"bytes"
	"fmt"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/cmdx"
	"flashcat.cloud/catpaw/pkg/filter"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const pluginName = "sfilter"

type Instance struct {
	config.InternalConfig

	Command       string          `toml:"command"`
	Timeout       config.Duration `toml:"timeout"`
	Check         string          `toml:"check"`
	FilterInclude []string        `toml:"filter_include"`
	FilterExclude []string        `toml:"filter_exclude"`

	filter filter.Filter
}

type SFilterPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *SFilterPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &SFilterPlugin{}
	})
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.Command) == 0 {
		return
	}

	if ins.Check == "" {
		logger.Logger.Warnln("configuration check is empty")
		return
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}

	if ins.filter == nil {
		if len(ins.FilterInclude) == 0 && len(ins.FilterExclude) == 0 {
			logger.Logger.Error("filter_include and filter_exclude are empty")
			return
		}

		var err error
		ins.filter, err = filter.NewIncludeExcludeFilter(ins.FilterInclude, ins.FilterExclude)
		if err != nil {
			logger.Logger.Warnw("failed to create filter", "error", err)
			return
		}
	}

	ins.gather(q, ins.Command)
}

func (ins *Instance) gather(q *safe.Queue[*types.Event], command string) {
	outbuf, errbuf, err := cmdx.CommandRun(command, time.Duration(ins.Timeout))
	if err != nil || len(errbuf) > 0 {
		logger.Logger.Errorw("failed to exec command", "command", command, "error", err, "stderr", string(errbuf), "stdout", string(outbuf))
		return
	}

	if len(outbuf) == 0 {
		logger.Logger.Warnw("exec command output is empty", "command", command)
		return
	}

	var bs bytes.Buffer
	var triggered bool

	bs.WriteString("[MD]")
	bs.WriteString(fmt.Sprintf("- filter_include: `%s`\n", ins.FilterInclude))
	bs.WriteString(fmt.Sprintf("- filter_exclude: `%s`\n", ins.FilterExclude))
	bs.WriteString("\n")
	bs.WriteString("\n")
	bs.WriteString("**matched lines**:\n")
	bs.WriteString("\n```")

	for _, line := range bytes.Split(outbuf, []byte("\n")) {
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

	if !triggered {
		q.PushFront(types.BuildEvent(map[string]string{"check": ins.Check}).SetTitleRule("$check").SetDescription("everything is ok"))
	} else {
		q.PushFront(types.BuildEvent(map[string]string{"check": ins.Check}).SetTitleRule("$check").SetDescription(bs.String()).SetEventStatus(ins.GetDefaultSeverity()))
	}
}
