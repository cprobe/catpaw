package sfilter

import (
	"bytes"
	"fmt"
	"io"
	osExec "os/exec"
	"runtime"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/cmdx"
	"flashcat.cloud/catpaw/pkg/filter"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/pkg/shell"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const (
	pluginName     string = "sfilter"
	maxStderrBytes int    = 512
)

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
	outbuf, errbuf, err := commandRun(command, time.Duration(ins.Timeout))
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

func commandRun(command string, timeout time.Duration) ([]byte, []byte, error) {
	splitCmd, err := shell.QuoteSplit(command)
	if err != nil || len(splitCmd) == 0 {
		return nil, nil, fmt.Errorf("exec: unable to parse command, %s", err)
	}

	cmd := osExec.Command(splitCmd[0], splitCmd[1:]...)

	var (
		out    bytes.Buffer
		stderr bytes.Buffer
	)
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	runError, runTimeout := cmdx.RunTimeout(cmd, timeout)
	if runTimeout {
		return nil, nil, fmt.Errorf("exec %s timeout", command)
	}

	out = removeWindowsCarriageReturns(out)
	if stderr.Len() > 0 {
		stderr = removeWindowsCarriageReturns(stderr)
		stderr = truncate(stderr)
	}

	return out.Bytes(), stderr.Bytes(), runError
}

func truncate(buf bytes.Buffer) bytes.Buffer {
	// Limit the number of bytes.
	didTruncate := false
	if buf.Len() > maxStderrBytes {
		buf.Truncate(maxStderrBytes)
		didTruncate = true
	}
	if i := bytes.IndexByte(buf.Bytes(), '\n'); i > 0 {
		// Only show truncation if the newline wasn't the last character.
		if i < buf.Len()-1 {
			didTruncate = true
		}
		buf.Truncate(i)
	}
	if didTruncate {
		//nolint:errcheck,revive // Will always return nil or panic
		buf.WriteString("...")
	}
	return buf
}

// removeWindowsCarriageReturns removes all carriage returns from the input if the
// OS is Windows. It does not return any errors.
func removeWindowsCarriageReturns(b bytes.Buffer) bytes.Buffer {
	if runtime.GOOS == "windows" {
		var buf bytes.Buffer
		for {
			byt, err := b.ReadBytes(0x0D)
			byt = bytes.TrimRight(byt, "\x0d")
			if len(byt) > 0 {
				_, _ = buf.Write(byt)
			}
			if err == io.EOF {
				return buf
			}
		}
	}
	return b
}
