package exec

import (
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
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
	plugins.Add("exec", func() plugins.Plugin {
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

	logger.Logger.Infof("exec: %v timeout: %v con: %v", ins.Commands, ins.Timeout, ins.Concurrency)
}
