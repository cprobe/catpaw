package config

import (
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/cprobe/catpaw/pkg/cfg"
	"github.com/jackpal/gateway"
	"github.com/toolkits/pkg/file"
)

type Global struct {
	Interval Duration          `toml:"interval"`
	Labels   map[string]string `toml:"labels"`
}

type LogConfig struct {
	Level  string                 `toml:"level"`
	Format string                 `toml:"format"`
	Output string                 `toml:"output"`
	Fields map[string]interface{} `toml:"fields"`
}

type FlashdutyConfig struct {
	IntegrationKey string   `toml:"integration_key"`
	BaseUrl        string   `toml:"base_url"`
	Timeout        Duration `toml:"timeout"`
	MaxRetries     int      `toml:"max_retries"`
}

type PagerDutyConfig struct {
	RoutingKey  string            `toml:"routing_key"`
	BaseUrl     string            `toml:"base_url"`
	SeverityMap map[string]string `toml:"severity_map"`
	Timeout     Duration          `toml:"timeout"`
	MaxRetries  int               `toml:"max_retries"`
}

type WebAPIConfig struct {
	URL        string            `toml:"url"`
	Method     string            `toml:"method"`
	Headers    map[string]string `toml:"headers"`
	Timeout    Duration          `toml:"timeout"`
	MaxRetries int               `toml:"max_retries"`
}

type ConsoleConfig struct {
	Enabled bool `toml:"enabled"`
}

type NotifyConfig struct {
	Console   *ConsoleConfig   `toml:"console"`
	Flashduty *FlashdutyConfig `toml:"flashduty"`
	PagerDuty *PagerDutyConfig `toml:"pagerduty"`
	WebAPI    *WebAPIConfig    `toml:"webapi"`
}

// ModelConfig defines connection and model-specific parameters for one AI model.
type ModelConfig struct {
	BaseURL       string                 `toml:"base_url"`
	APIKey        string                 `toml:"api_key"`
	Model         string                 `toml:"model"`
	MaxTokens     int                    `toml:"max_tokens"`
	ContextWindow int                    `toml:"context_window"`
	InputPrice    float64                `toml:"input_price"`
	OutputPrice   float64                `toml:"output_price"`
	ExtraBody     map[string]interface{} `toml:"extra_body"`
}

// AIConfig holds the full AI subsystem configuration.
// ModelPriority defines the failover order; Models maps profile names to ModelConfig.
type AIConfig struct {
	Enabled       bool                   `toml:"enabled"`
	ModelPriority []string               `toml:"model_priority"`
	Models        map[string]ModelConfig `toml:"models"`

	MaxRounds      int      `toml:"max_rounds"`
	RequestTimeout Duration `toml:"request_timeout"`

	MaxRetries   int      `toml:"max_retries"`
	RetryBackoff Duration `toml:"retry_backoff"`

	MaxConcurrentDiagnoses int    `toml:"max_concurrent_diagnoses"`
	QueueFullPolicy        string `toml:"queue_full_policy"`
	DailyTokenLimit        int    `toml:"daily_token_limit"`

	ToolTimeout     Duration `toml:"tool_timeout"`
	AggregateWindow Duration `toml:"aggregate_window"`

	DiagnoseRetention Duration `toml:"diagnose_retention"`
	DiagnoseMaxCount  int      `toml:"diagnose_max_count"`

	Language string `toml:"language"`

	MCP MCPConfig `toml:"mcp"`
}

// PrimaryModel returns the first model in the priority list.
// Panics if ModelPriority is empty or references a missing model — Validate
// should always be called before this.
func (c *AIConfig) PrimaryModel() ModelConfig {
	return c.Models[c.ModelPriority[0]]
}

// PrimaryModelName returns the name of the first model in the priority list.
func (c *AIConfig) PrimaryModelName() string {
	if len(c.ModelPriority) == 0 {
		return ""
	}
	return c.ModelPriority[0]
}

type ConfigType struct {
	ConfigDir string `toml:"-"`
	StateDir  string `toml:"-"`
	Plugins   string `toml:"-"`
	Loglevel  string `toml:"-"`

	Global    Global       `toml:"global"`
	LogConfig LogConfig    `toml:"log"`
	Notify    NotifyConfig `toml:"notify"`
	AI        AIConfig     `toml:"ai"`
}

var Config *ConfigType

func InitConfig(configDir string, interval int64, plugins, loglevel string) error {
	configFile := path.Join(configDir, "config.toml")
	if !file.IsExist(configFile) {
		return fmt.Errorf("configuration file(%s) not found", configFile)
	}

	Config = &ConfigType{
		ConfigDir: configDir,
		StateDir:  filepath.Join(filepath.Dir(configDir), "state.d"),
		Plugins:   plugins,
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

	Config.Notify.applyDefaults()

	Config.AI.applyDefaults()
	Config.AI.resolveAPIKeys()

	if Config.Global.Labels == nil {
		Config.Global.Labels = make(map[string]string)
	}

	builtins := HostBuiltins()
	Config.Global.Labels = expandLabels(Config.Global.Labels, builtins)

	return nil
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

func (c *AIConfig) applyDefaults() {
	if c.MaxRounds <= 0 {
		c.MaxRounds = 15
	}
	if time.Duration(c.RequestTimeout) == 0 {
		c.RequestTimeout = Duration(60 * time.Second)
	}
	if c.MaxRetries == 0 && c.Enabled {
		c.MaxRetries = 2
	}
	if time.Duration(c.RetryBackoff) == 0 {
		c.RetryBackoff = Duration(2 * time.Second)
	}
	if c.MaxConcurrentDiagnoses <= 0 {
		c.MaxConcurrentDiagnoses = 3
	}
	if c.QueueFullPolicy == "" {
		c.QueueFullPolicy = "drop"
	}
	if time.Duration(c.ToolTimeout) == 0 {
		c.ToolTimeout = Duration(10 * time.Second)
	}
	if time.Duration(c.AggregateWindow) == 0 {
		c.AggregateWindow = Duration(5 * time.Second)
	}
	if time.Duration(c.DiagnoseRetention) == 0 {
		c.DiagnoseRetention = Duration(7 * 24 * time.Hour)
	}
	if c.DiagnoseMaxCount <= 0 {
		c.DiagnoseMaxCount = 1000
	}
	if c.Language == "" {
		c.Language = "zh"
	}

	for name, m := range c.Models {
		if m.MaxTokens <= 0 {
			m.MaxTokens = 4000
		}
		if m.ContextWindow <= 0 {
			m.ContextWindow = 128000
		}
		c.Models[name] = m
	}
}

// resolveAPIKeys resolves ${ENV_VAR} references in all model API keys.
func (c *AIConfig) resolveAPIKeys() {
	for name, m := range c.Models {
		if strings.HasPrefix(m.APIKey, "${") && strings.HasSuffix(m.APIKey, "}") {
			envKey := m.APIKey[2 : len(m.APIKey)-1]
			m.APIKey = os.Getenv(envKey)
			c.Models[name] = m
		}
	}
}

func (c *AIConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.ModelPriority) == 0 {
		return fmt.Errorf("[ai] model_priority is required when enabled=true")
	}
	if len(c.Models) == 0 {
		return fmt.Errorf("[ai] at least one model must be configured in [ai.models]")
	}
	for _, name := range c.ModelPriority {
		m, ok := c.Models[name]
		if !ok {
			return fmt.Errorf("[ai] model_priority references unknown model %q", name)
		}
		if m.BaseURL == "" {
			return fmt.Errorf("[ai.models.%s] base_url is required", name)
		}
		if m.APIKey == "" {
			return fmt.Errorf("[ai.models.%s] api_key is required (supports ${ENV_VAR} syntax)", name)
		}
	}
	if c.QueueFullPolicy != "drop" && c.QueueFullPolicy != "wait" {
		return fmt.Errorf("[ai] queue_full_policy must be \"drop\" or \"wait\", got %q", c.QueueFullPolicy)
	}
	return nil
}

func (c *NotifyConfig) applyDefaults() {
	if c.Flashduty != nil {
		if c.Flashduty.BaseUrl == "" {
			c.Flashduty.BaseUrl = "https://api.flashcat.cloud/event/push/alert/standard"
		}
		if c.Flashduty.Timeout == 0 {
			c.Flashduty.Timeout = Duration(10 * time.Second)
		}
		if c.Flashduty.MaxRetries <= 0 {
			c.Flashduty.MaxRetries = 1
		}
	}
	if c.PagerDuty != nil {
		if c.PagerDuty.BaseUrl == "" {
			c.PagerDuty.BaseUrl = "https://events.pagerduty.com/v2/enqueue"
		}
		if c.PagerDuty.Timeout == 0 {
			c.PagerDuty.Timeout = Duration(10 * time.Second)
		}
		if c.PagerDuty.MaxRetries <= 0 {
			c.PagerDuty.MaxRetries = 1
		}
		if c.PagerDuty.SeverityMap == nil {
			c.PagerDuty.SeverityMap = map[string]string{
				"Critical": "critical",
				"Warning":  "warning",
				"Info":     "info",
			}
		}
	}
	if c.WebAPI != nil {
		method := strings.ToUpper(c.WebAPI.Method)
		if method != "PUT" {
			method = "POST"
		}
		c.WebAPI.Method = method
		if c.WebAPI.Timeout == 0 {
			c.WebAPI.Timeout = Duration(10 * time.Second)
		}
		if c.WebAPI.MaxRetries <= 0 {
			c.WebAPI.MaxRetries = 1
		}
		for k, v := range c.WebAPI.Headers {
			c.WebAPI.Headers[k] = os.Expand(v, func(key string) string {
				return os.Getenv(key)
			})
		}
	}
}

// expandLabels resolves ${HOSTNAME}, ${SHORT_HOSTNAME}, ${IP} and any
// environment variable references in label values.
func expandLabels(labels map[string]string, builtins map[string]string) map[string]string {
	for k, v := range labels {
		if strings.Contains(v, "$") {
			labels[k] = ExpandWithBuiltins(v, builtins)
		}
	}
	return labels
}
