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

type MatchCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	Command       string          `toml:"command"`
	Timeout       config.Duration `toml:"timeout"`
	FilterInclude []string        `toml:"filter_include"`
	FilterExclude []string        `toml:"filter_exclude"`
	MaxLines      int             `toml:"max_lines"`
	Match         MatchCheck      `toml:"match"`

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

func (ins *Instance) Init() error {
	if ins.Command == "" {
		return fmt.Errorf("command is empty")
	}

	if len(ins.FilterInclude) == 0 && len(ins.FilterExclude) == 0 {
		return fmt.Errorf("filter_include and filter_exclude cannot both be empty")
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}

	if ins.MaxLines <= 0 {
		ins.MaxLines = 10
	}

	if ins.Match.Severity == "" {
		ins.Match.Severity = types.EventStatusWarning
	}

	f, err := filter.NewIncludeExcludeFilter(ins.FilterInclude, ins.FilterExclude)
	if err != nil {
		return fmt.Errorf("failed to compile filter: %v", err)
	}
	ins.filter = f

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	outbuf, errbuf, err := cmdx.CommandRun(ins.Command, time.Duration(ins.Timeout))
	if err != nil || len(errbuf) > 0 {
		logger.Logger.Errorw("failed to exec command", "command", ins.Command, "error", err, "stderr", string(errbuf), "stdout", string(outbuf))
		return
	}

	if len(outbuf) == 0 {
		logger.Logger.Warnw("exec command output is empty", "command", ins.Command)
		return
	}

	var desc bytes.Buffer
	var triggered bool
	var matchCount int

	desc.WriteString("[MD]")
	desc.WriteString(fmt.Sprintf("- command: `%s`\n", ins.Command))
	desc.WriteString(fmt.Sprintf("- filter_include: `%v`\n", ins.FilterInclude))
	desc.WriteString(fmt.Sprintf("- filter_exclude: `%v`\n", ins.FilterExclude))
	desc.WriteString("\n")
	desc.WriteString("**matched lines**:\n")
	desc.WriteString("\n```\n")

	for _, line := range bytes.Split(outbuf, []byte("\n")) {
		if len(line) == 0 {
			continue
		}

		if !ins.filter.Match(string(line)) {
			continue
		}

		triggered = true
		matchCount++
		if matchCount <= ins.MaxLines {
			desc.Write(line)
			desc.Write([]byte("\n"))
		}
	}

	if matchCount > ins.MaxLines {
		desc.WriteString(fmt.Sprintf("... and %d more lines\n", matchCount-ins.MaxLines))
	}

	desc.WriteString("```")

	tr := ins.Match.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	e := types.BuildEvent(map[string]string{
		"check":  "sfilter::match",
		"target": ins.Command,
	}).SetTitleRule(tr)

	if !triggered {
		e.SetDescription("everything is ok")
	} else {
		e.SetEventStatus(ins.Match.Severity)
		e.SetDescription(desc.String())
	}

	q.PushFront(e)
}
