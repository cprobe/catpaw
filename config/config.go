package config

import (
	"fmt"
	"path"
	"time"

	"flashcat.cloud/catpaw/pkg/cfg"
	"github.com/toolkits/pkg/file"
)

type Global struct {
	PrintConfigs bool              `toml:"print_configs"`
	Interval     Duration          `toml:"interval"`
	Labels       map[string]string `toml:"labels"`
}

type LogConfig struct {
	Level  string                 `toml:"level"`
	Format string                 `toml:"format"`
	Output string                 `toml:"output"`
	Fields map[string]interface{} `toml:"fields"`
}

type Flashduty struct {
	Url     string   `toml:"url"`
	Timeout Duration `toml:"timeout"`
}

type ConfigType struct {
	ConfigDir string
	TestMode  bool
	Plugins   string

	Global    Global    `toml:"global"`
	LogConfig LogConfig `toml:"log"`
	Flashduty Flashduty `toml:"flashduty"`
}

var Config *ConfigType

func InitConfig(configDir string, testMode bool, interval int64, plugins string) error {
	configFile := path.Join(configDir, "config.toml")
	if !file.IsExist(configFile) {
		return fmt.Errorf("configuration file(%s) not found", configFile)
	}

	Config = &ConfigType{
		ConfigDir: configDir,
		TestMode:  testMode,
		Plugins:   plugins,
	}

	if err := cfg.LoadConfigByDir(configDir, Config); err != nil {
		return fmt.Errorf("failed to load configs of directory: %s error:%s", configDir, err)
	}

	if interval > 0 {
		Config.Global.Interval = Duration(time.Duration(interval) * time.Second)
	}

	if Config.LogConfig.Level == "" {
		Config.LogConfig.Level = "info"
	}

	if Config.LogConfig.Format == "" {
		Config.LogConfig.Format = "json"
	}

	if len(Config.LogConfig.Output) == 0 {
		Config.LogConfig.Output = "stdout"
	}

	if Config.LogConfig.Fields == nil {
		Config.LogConfig.Fields = make(map[string]interface{})
	}

	if Config.Flashduty.Timeout == 0 {
		Config.Flashduty.Timeout = Duration(10 * time.Second)
	}

	if Config.Global.Labels == nil {
		Config.Global.Labels = make(map[string]string)
	}

	return nil
}
