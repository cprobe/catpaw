package config

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/cprobe/catpaw/pkg/cfg"
	"github.com/jackpal/gateway"
	"github.com/toolkits/pkg/file"
)

type Global struct {
	Interval         Duration          `toml:"interval"`
	Labels           map[string]string `toml:"labels"`
	LabelHasHostname bool              `toml:"label_has_hostname"`
}

type LogConfig struct {
	Level  string                 `toml:"level"`
	Format string                 `toml:"format"`
	Output string                 `toml:"output"`
	Fields map[string]interface{} `toml:"fields"`
}

type Flashduty struct {
	Url        string       `toml:"url"`
	Timeout    Duration     `toml:"timeout"`
	MaxRetries int          `toml:"max_retries"`
	Client     *http.Client `toml:"-"`
}

type ConfigType struct {
	ConfigDir string `toml:"-"`
	TestMode  bool   `toml:"-"`
	Plugins   string `toml:"-"`
	Url       string `toml:"-"`
	Loglevel  string `toml:"-"`

	Global    Global    `toml:"global"`
	LogConfig LogConfig `toml:"log"`
	Flashduty Flashduty `toml:"flashduty"`
}

var Config *ConfigType

func InitConfig(configDir string, testMode bool, interval int64, plugins, url, loglevel string) error {
	configFile := path.Join(configDir, "config.toml")
	if !file.IsExist(configFile) {
		return fmt.Errorf("configuration file(%s) not found", configFile)
	}

	Config = &ConfigType{
		ConfigDir: configDir,
		TestMode:  testMode,
		Plugins:   plugins,
		Url:       url,
		Loglevel:  loglevel,
	}

	if err := cfg.LoadConfigByDir(configDir, Config); err != nil {
		return fmt.Errorf("failed to load configs of directory: %s error:%s", configDir, err)
	}

	if interval > 0 {
		Config.Global.Interval = Duration(time.Second * time.Duration(interval))
	}

	if Config.Global.Interval == 0 {
		Config.Global.Interval = Duration(30 * time.Second)
	}

	if Config.Loglevel != "" {
		Config.LogConfig.Level = Config.Loglevel
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

	if Config.Flashduty.MaxRetries <= 0 {
		Config.Flashduty.MaxRetries = 1
	}

	if Config.Url != "" {
		Config.Flashduty.Url = Config.Url
	}

	Config.Flashduty.Client = &http.Client{
		Timeout: time.Duration(Config.Flashduty.Timeout),
	}

	if Config.Global.Labels == nil {
		Config.Global.Labels = make(map[string]string)
	}

	for k, v := range Config.Global.Labels {
		if !strings.Contains(v, "$") {
			continue
		}

		if strings.Contains(v, "$hostname") {
			Config.Global.LabelHasHostname = true
		}

		if strings.Contains(v, "$ip") {
			ip, err := GetOutboundIP()
			if err != nil {
				return fmt.Errorf("failed to get outbound ip: %v", err)
			}
			Config.Global.Labels[k] = strings.ReplaceAll(Config.Global.Labels[k], "$ip", fmt.Sprint(ip))
		}

		Config.Global.Labels[k] = os.Expand(Config.Global.Labels[k], func(key string) string {
			if key == "hostname" {
				return "$hostname"
			}
			return GetEnv(key)
		})
	}

	return nil
}

func GetEnv(key string) string {
	v := os.Getenv(key)
	var envVarEscaper = strings.NewReplacer(
		`"`, `\"`,
		`\`, `\\`,
	)
	return envVarEscaper.Replace(v)
}

// Get preferred outbound ip of this machine
func GetOutboundIP() (net.IP, error) {
	gateway, err := gateway.DiscoverGateway()
	if err != nil {
		return nil, fmt.Errorf("failed to detect gateway: %v", err)
	}

	gatewayip := fmt.Sprint(gateway)
	if gatewayip == "" {
		return nil, fmt.Errorf("failed to detect gateway: empty")
	}

	conn, err := net.Dial("udp", gatewayip+":80")
	if err != nil {
		return nil, fmt.Errorf("failed to get outbound ip: %v", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP, nil
}
