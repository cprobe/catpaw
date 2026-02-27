package journaltail

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/cmdx"
	"flashcat.cloud/catpaw/pkg/filter"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const pluginName = "journaltail"

type MatchCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	FilterInclude []string        `toml:"filter_include"`
	FilterExclude []string        `toml:"filter_exclude"`
	MaxLines      int             `toml:"max_lines"`
	Timeout       config.Duration `toml:"timeout"`
	Match         MatchCheck      `toml:"match"`

	filter   filter.Filter
	bin      string
	lastScan time.Time
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

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("journaltail plugin only supports linux (current: %s)", runtime.GOOS)
	}

	if len(ins.FilterInclude) == 0 && len(ins.FilterExclude) == 0 {
		return fmt.Errorf("filter_include and filter_exclude cannot both be empty")
	}

	if ins.MaxLines <= 0 {
		ins.MaxLines = 10
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(30 * time.Second)
	}

	f, err := filter.NewIncludeExcludeFilter(ins.FilterInclude, ins.FilterExclude)
	if err != nil {
		return fmt.Errorf("failed to compile filter: %v", err)
	}
	ins.filter = f

	bin, err := exec.LookPath("journalctl")
	if err != nil {
		return fmt.Errorf("journalctl not found: %v", err)
	}
	ins.bin = bin
	ins.lastScan = time.Now()

	if ins.Match.Severity == "" {
		ins.Match.Severity = types.EventStatusWarning
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	now := time.Now()
	sinceArg := ins.lastScan.Format("2006-01-02 15:04:05")
	ins.lastScan = now

	cmd := exec.Command(ins.bin, "--since", sinceArg, "--no-pager", "--no-tail")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	target := strings.Join(ins.FilterInclude, ",")

	runErr, timedOut := cmdx.RunTimeout(cmd, time.Duration(ins.Timeout))
	if timedOut {
		logger.Logger.Errorw("journalctl timed out",
			"timeout", time.Duration(ins.Timeout).String(),
			"target", target,
		)
		return
	}
	if runErr != nil {
		logger.Logger.Errorw("journalctl exec fail",
			"error", runErr,
			"stderr", stderr.String(),
			"target", target,
		)
		return
	}

	var desc bytes.Buffer
	var triggered bool
	var matchCount int

	desc.WriteString("[MD]")
	desc.WriteString(fmt.Sprintf("- target: `%s`\n", target))
	desc.WriteString(fmt.Sprintf("- since: `%s`\n", sinceArg))
	desc.WriteString(fmt.Sprintf("- filter_include: `%v`\n", ins.FilterInclude))
	desc.WriteString(fmt.Sprintf("- filter_exclude: `%v`\n", ins.FilterExclude))
	desc.WriteString("\n")
	desc.WriteString("**matched lines**:\n")
	desc.WriteString("\n```\n")

	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
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
		"check":  "journaltail::match",
		"target": target,
	}).SetTitleRule(tr)

	if !triggered {
		q.PushFront(e)
		return
	}

	e.SetEventStatus(ins.Match.Severity)
	e.SetDescription(desc.String())
	q.PushFront(e)
}
