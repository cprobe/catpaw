package exec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	osExec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/cmdx"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const (
	pluginName     string = "exec"
	maxStderrBytes int    = 512
)

type Instance struct {
	config.InternalConfig

	Commands    []string        `toml:"commands"`
	Timeout     config.Duration `toml:"timeout"`
	Concurrency int             `toml:"concurrency"`
}

type Exec struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *Exec) IsSystemPlugin() bool {
	return false
}

func (p *Exec) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &Exec{}
	})
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.Commands) == 0 {
		return
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}

	if ins.Concurrency == 0 {
		ins.Concurrency = 5
	}

	var commands []string
	for _, pattern := range ins.Commands {
		cmdAndArgs := strings.SplitN(pattern, " ", 2)
		if len(cmdAndArgs) == 0 {
			continue
		}

		matches, err := filepath.Glob(cmdAndArgs[0])
		if err != nil {
			logger.Logger.Errorw("failed to get filepath glob", "error", err, "pattern", cmdAndArgs[0])
			continue
		}

		if len(matches) == 0 {
			// There were no matches with the glob pattern, so let's assume
			// that the command is in PATH and just run it as it is
			commands = append(commands, pattern)
		} else {
			// There were matches, so we'll append each match together with
			// the arguments to the commands slice
			for _, match := range matches {
				if len(cmdAndArgs) == 1 {
					commands = append(commands, match)
				} else {
					commands = append(commands,
						strings.Join([]string{match, cmdAndArgs[1]}, " "))
				}
			}
		}
	}

	if len(commands) == 0 {
		logger.Logger.Warnln("no commands after parse")
		return
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)
	for _, command := range commands {
		wg.Add(1)
		se.Acquire()
		go func(command string) {
			defer func() {
				se.Release()
				wg.Done()
			}()
			ins.gather(q, command)
		}(command)
	}
	wg.Wait()
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

	var events []*types.Event
	if err := json.Unmarshal(outbuf, &events); err != nil {
		logger.Logger.Errorw("failed to unmarshal command output", "command", command, "error", err, "output", string(outbuf))
		return
	}

	for i := range events {
		q.PushFront(events[i])
	}
}

func commandRun(command string, timeout time.Duration) ([]byte, []byte, error) {
	splitCmd, err := QuoteSplit(command)
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
