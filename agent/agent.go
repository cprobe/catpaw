package agent

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/plugins"
	"github.com/BurntSushi/toml"
	"github.com/toolkits/pkg/file"

	// auto registry
	_ "flashcat.cloud/catpaw/plugins/http"
)

type PluginConfig struct {
	Digest      string
	FileContent []byte
}

type Agent struct {
	pluginFilters map[string]struct{}
	pluginConfigs map[string]*PluginConfig
	pluginRunners map[string]*PluginRunner
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

	pcs, err := a.LoadFileConfigs()
	if err != nil {
		logger.Logger.Error("load file configs fail:", err)
		return
	}

	a.pluginConfigs = pcs

	for name, pc := range a.pluginConfigs {
		creator, has := plugins.PluginCreators[name]
		if !has {
			logger.Logger.Infof("plugin %s not supported", name)
			continue
		}

		pluginObject := creator()
		err = toml.Unmarshal(pc.FileContent, pluginObject)
		if err != nil {
			logger.Logger.Error("unmarshal plugin config fail:", err)
			continue
		}

		runner := newPluginRunner(name, pluginObject)
		go runner.start()
		a.pluginRunners[name] = runner
	}

	logger.Logger.Info("agent started")
}

func (a *Agent) Stop() {
	logger.Logger.Info("agent stopping")
	logger.Logger.Info("agent stopped")
}

func (a *Agent) Reload() {
	logger.Logger.Info("agent reloading")
	logger.Logger.Info("agent reloaded")
}

func parseFilter(filterStr string) map[string]struct{} {
	filters := strings.Split(filterStr, ":")
	filtermap := make(map[string]struct{})
	for i := 0; i < len(filters); i++ {
		if strings.TrimSpace(filters[i]) == "" {
			continue
		}
		filtermap[filters[i]] = struct{}{}
	}
	return filtermap
}

func (a *Agent) LoadFileConfigs() (map[string]*PluginConfig, error) {
	dirs, err := file.DirsUnder(config.Config.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get config dirs: %v", err)
	}

	ret := make(map[string]*PluginConfig)

	for _, dir := range dirs {
		if !strings.HasPrefix(dir, "p.") {
			continue
		}

		// use this as map key
		name := dir[len("p."):]

		if len(a.pluginFilters) > 0 {
			// need filter by --plugins
			_, has := a.pluginFilters[name]
			if !has {
				continue
			}
		}

		pluginDir := path.Join(config.Config.ConfigDir, dir)
		files, err := file.FilesUnder(pluginDir)
		if err != nil {
			return nil, fmt.Errorf("failed to list files under %s: %v", pluginDir, err)
		}

		if len(files) == 0 {
			continue
		}

		sort.Strings(files)

		var maxmt int64
		var bytes []byte
		for i := 0; i < len(files); i++ {
			filepath := path.Join(pluginDir, files[i])
			mtime, err := file.FileMTime(filepath)
			if err != nil {
				return nil, fmt.Errorf("failed to get mtime of %s: %v", filepath, err)
			}

			if mtime > maxmt {
				maxmt = mtime
			}

			if i > 0 {
				bytes = append(bytes, '\n')
				bytes = append(bytes, '\n')
			}

			bs, err := file.ReadBytes(filepath)
			if err != nil {
				return nil, fmt.Errorf("failed to read %s: %v", filepath, err)
			}

			bytes = append(bytes, bs...)
		}

		ret[name] = &PluginConfig{
			Digest:      fmt.Sprint(maxmt),
			FileContent: bytes,
		}
	}

	return ret, nil
}
