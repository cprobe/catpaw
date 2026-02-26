package exec

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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

const pluginName = "exec"

type Instance struct {
	config.InternalConfig

	Commands    []string        `toml:"commands"`
	Timeout     config.Duration `toml:"timeout"`
	Concurrency int             `toml:"concurrency"`
}

type ExecPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *ExecPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &ExecPlugin{}
	})
}

func (ins *Instance) Init() error {
	if len(ins.Commands) == 0 {
		return fmt.Errorf("commands is empty")
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}

	if ins.Concurrency == 0 {
		ins.Concurrency = 5
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
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
			commands = append(commands, pattern)
		} else {
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
		go func(command string) {
			se.Acquire()
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
	outbuf, errbuf, err := cmdx.CommandRun(command, time.Duration(ins.Timeout))
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
