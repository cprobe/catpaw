package journaltail

import (
	"bytes"
	"fmt"
	"os/exec"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const (
	pluginName string = "journaltail"
)

type Instance struct {
	config.InternalConfig

	TimeSpan    string   `toml:"time_span"`
	Keywords    []string `toml:"keywords"`
	Check       string   `toml:"check"`
	Concurrency int      `toml:"concurrency"`
}

type Journaltail struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *Journaltail) IsSystemPlugin() bool {
	return false
}

func (p *Journaltail) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &Journaltail{}
	})
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.TimeSpan == "" {
		ins.TimeSpan = "1m"
	}

	if len(ins.Keywords) == 0 {
		logger.Logger.Error("keywords is empty")
		return
	}

	if ins.Check == "" {
		logger.Logger.Error("check is empty")
		return
	}

	if ins.Concurrency == 0 {
		ins.Concurrency = 5
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

	out, err := exec.Command(bin, "--since", ins.TimeSpan, "--no-pager", "--no-tail").Output()
	if err != nil {
		logger.Logger.Error("exec journalctl fail: ", err)
		return
	}

	for _, line := range bytes.Split(out, []byte("\n")) {
		for _, keyword := range ins.Keywords {
			if bytes.Contains(line, []byte(keyword)) {
				fmt.Println(string(line))
			}
		}
	}

}
