package scriptfilter

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/pkg/cmdx"
	"flashcat.cloud/catpaw/pkg/filter"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/pkg/shell"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const pluginName = "scriptfilter"

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

	includeFilter filter.Filter
	excludeFilter filter.Filter
	target        string
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
	ins.Command = strings.TrimSpace(ins.Command)
	if ins.Command == "" {
		return nil
	}

	if len(ins.FilterInclude) == 0 {
		return fmt.Errorf("filter_include must be configured")
	}

	if ins.Timeout < 0 {
		return fmt.Errorf("timeout must be non-negative")
	}
	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}

	if ins.MaxLines <= 0 {
		ins.MaxLines = 10
	}

	if ins.Match.Severity == "" {
		ins.Match.Severity = types.EventStatusWarning
	} else if !types.EventStatusValid(ins.Match.Severity) {
		return fmt.Errorf("invalid severity %q, must be one of: Critical, Warning, Info, Ok", ins.Match.Severity)
	}

	var err error

	ins.includeFilter, err = filter.Compile(ins.FilterInclude)
	if err != nil {
		return fmt.Errorf("failed to compile filter_include: %v", err)
	}

	ins.excludeFilter, err = filter.Compile(ins.FilterExclude)
	if err != nil {
		return fmt.Errorf("failed to compile filter_exclude: %v", err)
	}

	ins.target = buildTarget(ins.Command)

	return nil
}

// buildTarget extracts the basename of the command (without path and arguments).
func buildTarget(command string) string {
	parts, err := shell.QuoteSplit(command)
	if err != nil || len(parts) == 0 {
		return command
	}
	return filepath.Base(parts[0])
}

func (ins *Instance) matchLine(line string) bool {
	if ins.includeFilter != nil {
		if !ins.includeFilter.Match(line) {
			return false
		}
	}

	if ins.excludeFilter != nil {
		if ins.excludeFilter.Match(line) {
			return false
		}
	}

	return true
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.Command == "" {
		return
	}

	outbuf, errbuf, err := cmdx.CommandRun(ins.Command, time.Duration(ins.Timeout))
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// Real execution failure (not found, timeout, permission denied, etc.)
			q.PushFront(ins.buildErrorEvent(formatExecError(err, errbuf)))
			return
		}
		// Non-zero exit: command ran successfully, just exited non-zero.
		// Proceed to process stdout — many monitoring scripts use exit codes to signal issues.
	}

	lines := splitLines(outbuf)

	var matched []string
	for _, line := range lines {
		if ins.matchLine(line) {
			matched = append(matched, line)
		}
	}

	tr := ins.Match.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	e := types.BuildEvent(map[string]string{
		"check":  "scriptfilter::match",
		"target": ins.target,
	}).SetTitleRule(tr)

	if len(matched) == 0 {
		q.PushFront(e)
		return
	}

	var desc bytes.Buffer
	desc.WriteString("[MD]\n")
	fmt.Fprintf(&desc, "- **target**: %s\n", ins.target)
	fmt.Fprintf(&desc, "- **command**: `%s`\n", ins.Command)
	desc.WriteString("\n**matched lines**:\n\n```\n")

	for i, line := range matched {
		if i >= ins.MaxLines {
			break
		}
		desc.WriteString(line)
		desc.WriteByte('\n')
	}

	if len(matched) > ins.MaxLines {
		fmt.Fprintf(&desc, "... and %d more lines\n", len(matched)-ins.MaxLines)
	}
	desc.WriteString("```")

	e.SetEventStatus(ins.Match.Severity)
	e.SetDescription(desc.String())
	q.PushFront(e)
}

func (ins *Instance) buildErrorEvent(errMsg string) *types.Event {
	tr := ins.Match.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	return types.BuildEvent(map[string]string{
		"check":  "scriptfilter::match",
		"target": ins.target,
	}).SetTitleRule(tr).
		SetEventStatus(types.EventStatusCritical).
		SetDescription(fmt.Sprintf("[MD]\n- **target**: %s\n- **error**: %s\n", ins.target, errMsg))
}

// splitLines splits output into non-empty lines.
func splitLines(data []byte) []string {
	var lines []string
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(raw) > 0 {
			lines = append(lines, string(raw))
		}
	}
	return lines
}

func formatExecError(err error, stderr []byte) string {
	msg := fmt.Sprintf("command exec failed: %v", err)
	s := strings.TrimSpace(string(stderr))
	if s != "" {
		if len(s) > 256 {
			s = s[:256] + "..."
		}
		msg += fmt.Sprintf(" (stderr: %s)", s)
	}
	return msg
}
