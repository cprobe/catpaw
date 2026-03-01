package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/choice"
	"github.com/cprobe/catpaw/plugins"
	"github.com/BurntSushi/toml"
	"github.com/toolkits/pkg/file"

	// auto registry
	_ "github.com/cprobe/catpaw/plugins/cert"
	_ "github.com/cprobe/catpaw/plugins/conntrack"
	_ "github.com/cprobe/catpaw/plugins/cpu"
	_ "github.com/cprobe/catpaw/plugins/disk"
	_ "github.com/cprobe/catpaw/plugins/dns"
	_ "github.com/cprobe/catpaw/plugins/docker"
	_ "github.com/cprobe/catpaw/plugins/exec"
	_ "github.com/cprobe/catpaw/plugins/filecheck"
	_ "github.com/cprobe/catpaw/plugins/filefd"
	_ "github.com/cprobe/catpaw/plugins/http"
	_ "github.com/cprobe/catpaw/plugins/journaltail"
	_ "github.com/cprobe/catpaw/plugins/logfile"
	_ "github.com/cprobe/catpaw/plugins/mem"
	_ "github.com/cprobe/catpaw/plugins/neigh"
	_ "github.com/cprobe/catpaw/plugins/net"
	_ "github.com/cprobe/catpaw/plugins/ntp"
	_ "github.com/cprobe/catpaw/plugins/ping"
	_ "github.com/cprobe/catpaw/plugins/procfd"
	_ "github.com/cprobe/catpaw/plugins/procnum"
	_ "github.com/cprobe/catpaw/plugins/scriptfilter"
	_ "github.com/cprobe/catpaw/plugins/sockstat"
	_ "github.com/cprobe/catpaw/plugins/sysctl"
	_ "github.com/cprobe/catpaw/plugins/systemd"
	_ "github.com/cprobe/catpaw/plugins/uptime"
)

type PluginConfig struct {
	Source      string // file || http
	Digest      string
	FileContent []byte
}

type Agent struct {
	pluginFilters map[string]struct{}
	pluginConfigs map[string]*PluginConfig
	pluginRunners map[string]*PluginRunner
	sync.RWMutex
}

func New() *Agent {
	return &Agent{
		pluginFilters: parseFilter(config.Config.Plugins),
		pluginConfigs: make(map[string]*PluginConfig),
		pluginRunners: make(map[string]*PluginRunner),
	}
}

func (a *Agent) Start() {
	logger.Logger.Info("agent starting")

	pcs, err := loadFileConfigs()
	if err != nil {
		logger.Logger.Errorw("load file configs fail", "error", err)
		return
	}

	for name, pc := range pcs {
		a.LoadPlugin(name, pc)
	}

	logger.Logger.Info("agent started")
}

func (a *Agent) LoadPlugin(name string, pc *PluginConfig) {
	if len(a.pluginFilters) > 0 {
		// need filter by --plugins
		_, has := a.pluginFilters[name]
		if !has {
			return
		}
	}

	logger.Logger.Infow("loading plugin", "plugin", name)

	creator, has := plugins.PluginCreators[name]
	if !has {
		logger.Logger.Infow("plugin not supported", "plugin", name)
		return
	}

	pluginObject := creator()
	err := toml.Unmarshal(pc.FileContent, pluginObject)
	if err != nil {
		logger.Logger.Errorw("unmarshal plugin config fail", "plugin", name, "error", err)
		return
	}

	// structs will have value after toml.Unmarshal
	// apply partial configuration if some fields are not set
	err = plugins.MayApplyPartials(pluginObject)
	if err != nil {
		logger.Logger.Errorw("apply partial config fail", "plugin", name, "error", err)
		return
	}

	runner := newPluginRunner(name, pluginObject)
	runner.start()

	a.Lock()
	a.pluginRunners[name] = runner
	a.pluginConfigs[name] = pc
	a.Unlock()
}

func (a *Agent) DelPlugin(name string) {
	a.Lock()
	defer a.Unlock()

	if runner, has := a.pluginRunners[name]; has {
		runner.stop()
		delete(a.pluginRunners, name)
		delete(a.pluginConfigs, name)
	}
}

func (a *Agent) RunningPlugins() []string {
	a.RLock()
	defer a.RUnlock()

	cnt := len(a.pluginRunners)
	ret := make([]string, 0, cnt)

	for name := range a.pluginRunners {
		ret = append(ret, name)
	}

	return ret
}

func (a *Agent) GetPluginConfig(name string) *PluginConfig {
	a.RLock()
	defer a.RUnlock()

	return a.pluginConfigs[name]
}

func (a *Agent) Stop() {
	logger.Logger.Info("agent stopping")

	a.Lock()
	defer a.Unlock()

	for name := range a.pluginRunners {
		a.pluginRunners[name].stop()
		delete(a.pluginRunners, name)
		delete(a.pluginConfigs, name)
	}

	logger.Logger.Info("agent stopped")
}

func (a *Agent) HandleChangedPlugin(names []string) {
	for _, name := range names {
		pc := a.GetPluginConfig(name)
		if pc.Source != "file" {
			continue
		}

		mtime, content, err := readPluginDir(name)
		if err != nil {
			logger.Logger.Errorw("read plugin dir fail", "plugin", name, "error", err)
			continue
		}

		if mtime == -1 || len(content) == 0 {
			a.DelPlugin(name)
			continue
		}

		if pc.Digest == fmt.Sprint(mtime) {
			continue
		}

		a.DelPlugin(name)
		a.LoadPlugin(name, &PluginConfig{
			Source:      "file",
			Digest:      fmt.Sprint(mtime),
			FileContent: content,
		})
	}
}

func (a *Agent) Reload() {
	logger.Logger.Info("agent reloading")

	names := a.RunningPlugins()
	a.HandleChangedPlugin(names)
	a.HandleNewPlugin(names)

	logger.Logger.Info("agent reloaded")
}

func (a *Agent) HandleNewPlugin(names []string) {
	dirs, err := file.DirsUnder(config.Config.ConfigDir)
	if err != nil {
		logger.Logger.Errorw("failed to get config dirs", "error", err)
		return
	}

	for _, dir := range dirs {
		if !strings.HasPrefix(dir, "p.") {
			continue
		}

		name := dir[len("p."):]

		if choice.Contains(name, names) {
			continue
		}

		mtime, content, err := readPluginDir(name)
		if err != nil {
			logger.Logger.Errorw("read plugin dir fail", "plugin", name, "error", err)
			continue
		}

		if mtime == -1 || len(content) == 0 {
			continue
		}

		a.LoadPlugin(name, &PluginConfig{
			Source:      "file",
			Digest:      fmt.Sprint(mtime),
			FileContent: content,
		})
	}
}
