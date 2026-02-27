package journaltail

import (
	"bytes"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/cmdx"
	"github.com/cprobe/catpaw/pkg/filter"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

const pluginName = "journaltail"

type MatchCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	// journalctl native pre-filters
	Units    []string `toml:"units"`
	Priority string   `toml:"priority"`

	// Line-level filtering: glob patterns and /regex/ patterns can be mixed.
	// A pattern wrapped in / is treated as regex, e.g. "/OOM|oom-killer/"
	FilterInclude []string `toml:"filter_include"`
	FilterExclude []string `toml:"filter_exclude"`

	MaxLines int             `toml:"max_lines"`
	Timeout  config.Duration `toml:"timeout"`
	Match    MatchCheck      `toml:"match"`

	includeFilter filter.Filter
	excludeFilter filter.Filter

	bin       string
	cursor    string
	initSince string // formatted startup time for first --since
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

	if len(ins.FilterInclude) == 0 {
		return fmt.Errorf("filter_include must be configured")
	}

	if ins.MaxLines <= 0 {
		ins.MaxLines = 10
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(30 * time.Second)
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

	bin, err := exec.LookPath("journalctl")
	if err != nil {
		return fmt.Errorf("journalctl not found: %v", err)
	}
	ins.bin = bin

	if ins.Match.Severity == "" {
		ins.Match.Severity = types.EventStatusWarning
	} else if !types.EventStatusValid(ins.Match.Severity) {
		return fmt.Errorf("invalid severity %q, must be one of: Critical, Warning, Info, Ok", ins.Match.Severity)
	}

	ins.initSince = time.Now().Format("2006-01-02 15:04:05")

	return nil
}

func (ins *Instance) buildTarget() string {
	if len(ins.Units) > 0 {
		return strings.Join(ins.Units, ",")
	}
	if len(ins.FilterInclude) == 1 {
		return ins.FilterInclude[0]
	}
	if len(ins.FilterInclude) > 1 {
		return fmt.Sprintf("%s(+%d)", ins.FilterInclude[0], len(ins.FilterInclude)-1)
	}
	return "journaltail"
}

func (ins *Instance) buildArgs() []string {
	args := []string{"--no-pager", "--no-tail", "--show-cursor"}

	if ins.cursor != "" {
		args = append(args, "--after-cursor", ins.cursor)
	} else {
		args = append(args, "--since", ins.initSince)
	}

	for _, u := range ins.Units {
		args = append(args, "--unit", u)
	}

	if ins.Priority != "" {
		args = append(args, "--priority", ins.Priority)
	}

	return args
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
	target := ins.buildTarget()
	args := ins.buildArgs()

	cmd := exec.Command(ins.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, time.Duration(ins.Timeout))

	// P2/P3: on failure, generate alert event and do NOT update cursor
	if timedOut {
		q.PushFront(ins.buildErrorEvent(target, fmt.Sprintf("journalctl timed out after %s", time.Duration(ins.Timeout))))
		return
	}
	if runErr != nil {
		q.PushFront(ins.buildErrorEvent(target, fmt.Sprintf("journalctl exec failed: %v (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))))
		return
	}

	// Parse output and extract cursor from the last line
	output := stdout.Bytes()
	newCursor := extractCursor(output)
	lines := extractLines(output)

	var matched []string
	for _, line := range lines {
		if ins.matchLine(line) {
			matched = append(matched, line)
		}
	}

	if newCursor != "" {
		ins.cursor = newCursor
	}

	tr := ins.Match.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	labels := map[string]string{
		"check":  "journaltail::match",
		"target": target,
	}
	if len(ins.Units) > 0 {
		labels[types.AttrPrefix+"units"] = strings.Join(ins.Units, ", ")
	}
	if ins.Priority != "" {
		labels[types.AttrPrefix+"priority"] = ins.Priority
	}

	e := types.BuildEvent(labels).SetTitleRule(tr)

	if len(matched) == 0 {
		q.PushFront(e)
		return
	}

	labels[types.AttrPrefix+"matched_count"] = fmt.Sprintf("%d", len(matched))

	var desc strings.Builder
	fmt.Fprintf(&desc, "matched %d lines:\n", len(matched))
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

	e.SetEventStatus(ins.Match.Severity)
	e.SetDescription(desc.String())
	q.PushFront(e)
}

func (ins *Instance) buildErrorEvent(target, errMsg string) *types.Event {
	tr := ins.Match.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	return types.BuildEvent(map[string]string{
		"check":  "journaltail::match",
		"target": target,
	}).SetTitleRule(tr).
		SetEventStatus(types.EventStatusCritical).
		SetDescription(errMsg)
}

// extractCursor parses the journal cursor from `--show-cursor` output.
// The cursor line looks like: "-- cursor: s=...;i=...;b=...;m=...;t=...;x=..."
func extractCursor(output []byte) string {
	const prefix = "-- cursor: "
	idx := bytes.LastIndex(output, []byte(prefix))
	if idx < 0 {
		return ""
	}
	rest := output[idx+len(prefix):]
	if end := bytes.IndexByte(rest, '\n'); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(string(rest))
}

// extractLines returns all non-empty, non-cursor lines from journalctl output.
func extractLines(output []byte) []string {
	var lines []string
	for _, raw := range bytes.Split(output, []byte("\n")) {
		line := string(raw)
		if line == "" || strings.HasPrefix(line, "-- cursor: ") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}
