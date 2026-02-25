package agent

import (
	"fmt"
	"strings"
	"sync"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/choice"
	"flashcat.cloud/catpaw/plugins"
	"github.com/BurntSushi/toml"
	"github.com/toolkits/pkg/file"

	// auto registry
	_ "flashcat.cloud/catpaw/plugins/exec"
	_ "flashcat.cloud/catpaw/plugins/filechange"
	_ "flashcat.cloud/catpaw/plugins/http"
	_ "flashcat.cloud/catpaw/plugins/journaltail"
	_ "flashcat.cloud/catpaw/plugins/mtime"
	_ "flashcat.cloud/catpaw/plugins/net"
	_ "flashcat.cloud/catpaw/plugins/ping"
	_ "flashcat.cloud/catpaw/plugins/procnum"
	_ "flashcat.cloud/catpaw/plugins/sfilter"
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
			// not supported
			continue
		}

		mtime, err := getMTime(name)
		if err != nil {
			logger.Logger.Errorw("get mtime fail", "plugin", name, "error", err)
			continue
		}

		if mtime == -1 {
			// files deleted
			a.DelPlugin(name)
			continue
		}

		if pc.Digest == fmt.Sprint(mtime) {
			// not changed
			continue
		}

		// configuration changed
		// delete old plugin
		a.DelPlugin(name)

		bs, err := getFileContent(name)
		if err != nil {
			logger.Logger.Errorw("get file content fail", "plugin", name, "error", err)
			continue
		}

		if bs == nil {
			// files deleted
			continue
		}

		a.LoadPlugin(name, &PluginConfig{
			Source:      "file",
			Digest:      fmt.Sprint(mtime),
			FileContent: bs,
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
			// already running
			continue
		}

		mtime, err := getMTime(name)
		if err != nil {
			logger.Logger.Errorw("get mtime fail", "error", err)
			continue
		}

		if mtime == -1 {
			continue
		}

		bs, err := getFileContent(name)
		if err != nil {
			logger.Logger.Errorw("get file content fail", "error", err)
			continue
		}

		if bs == nil {
			continue
		}

		a.LoadPlugin(name, &PluginConfig{
			Source:      "file",
			Digest:      fmt.Sprint(mtime),
			FileContent: bs,
		})
	}
}
