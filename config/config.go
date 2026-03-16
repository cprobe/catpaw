package config

import (
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
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
	// Provider selects the backend: "" or "openai" for OpenAI-compatible,
	// "bedrock" for AWS Bedrock Converse API with SigV4 auth.
	Provider string `toml:"provider"`

	BaseURL             string                 `toml:"base_url"`
	APIKey              string                 `toml:"api_key"`
	Model               string                 `toml:"model"`
	MaxTokens           int                    `toml:"max_tokens"`
	MaxCompletionTokens int                    `toml:"max_completion_tokens"`
	ContextWindow       int                    `toml:"context_window"`
	InputPrice          float64                `toml:"input_price"`
	OutputPrice         float64                `toml:"output_price"`
	ExtraBody           map[string]interface{} `toml:"extra_body"`
}

// GatewayConfig defines the server-backed AI gateway connection.
type GatewayConfig struct {
	Enabled          bool     `toml:"enabled"`
	BaseURL          string   `toml:"base_url"`
	AgentToken       string   `toml:"agent_token"`
	RequestTimeout   Duration `toml:"request_timeout"`
	MaxRetries       int      `toml:"max_retries"`
	FallbackToDirect bool     `toml:"fallback_to_direct"`
}

// ExtraStr returns a string value from ExtraBody, or empty if missing.
func (m ModelConfig) ExtraStr(key string) string {
	if m.ExtraBody == nil {
		return ""
	}
	v, ok := m.ExtraBody[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// AIConfig holds the full AI subsystem configuration.
// ModelPriority defines the failover order; Models maps profile names to ModelConfig.
type AIConfig struct {
	Enabled       bool                   `toml:"enabled"`
	ModelPriority []string               `toml:"model_priority"`
	Models        map[string]ModelConfig `toml:"models"`
	Gateway       GatewayConfig          `toml:"gateway"`

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

// PrimaryModel returns the first configured local model in the priority list.
// Returns the zero value when no local model is configured.
func (c *AIConfig) PrimaryModel() ModelConfig {
	if len(c.ModelPriority) == 0 || len(c.Models) == 0 {
		return ModelConfig{}
	}
	return c.Models[c.ModelPriority[0]]
}

// PrimaryModelName returns the name of the first model in the priority list.
func (c *AIConfig) PrimaryModelName() string {
	if len(c.ModelPriority) == 0 || len(c.Models) == 0 {
		return ""
	}
	return c.ModelPriority[0]
}

// ContextWindowLimit returns 80% of the primary model's context window size,
// used as the safe upper bound for message token budgeting. Returns 0 if no
// models are configured.
func (c *AIConfig) ContextWindowLimit() int {
	if len(c.ModelPriority) == 0 || len(c.Models) == 0 {
		return 0
	}
	cw := c.PrimaryModel().ContextWindow
	if cw <= 0 {
		cw = 128000
	}
	return cw * 80 / 100
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
	Server    ServerConfig `toml:"server"`
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

	builtins := HostBuiltinsWithoutIP()
	Config.Global.Labels = resolveGlobalLabels(Config.Global.Labels, builtins)

	Config.Server.resolve()
	if Config.Server.Enabled && Config.Server.Address == "" {
		return fmt.Errorf("[server] address is required when enabled=true")
	}
	if err := Config.resolveGatewayConfig(); err != nil {
		return err
	}

	// When server is enabled, alert events are sent to server and need these labels.
	// When server is disabled, do not require them so catpaw can run in pure local mode.
	if Config.Server.Enabled {
		if err := validateRequiredLabels(Config.Global.Labels); err != nil {
			return err
		}
	}

	return nil
}

func (c *ConfigType) resolveGatewayConfig() error {
	if !c.AI.Gateway.Enabled {
		return nil
	}
	if c.AI.Gateway.AgentToken == "" {
		c.AI.Gateway.AgentToken = c.Server.AgentToken
	}
	if c.AI.Gateway.BaseURL != "" {
		c.AI.Gateway.BaseURL = strings.TrimRight(c.AI.Gateway.BaseURL, "/")
		return nil
	}
	if c.Server.Address == "" {
		return nil
	}
	derived, err := c.Server.GatewayBaseURL()
	if err != nil {
		return fmt.Errorf("resolve [ai.gateway] from [server]: %w", err)
	}
	c.AI.Gateway.BaseURL = derived
	return nil
}

// Required global labels when server is enabled; all must be present and non-empty after expansion.
var requiredGlobalLabels = []string{"from_agent", "from_hostname", "from_hostip"}

func validateRequiredLabels(labels map[string]string) error {
	for _, key := range requiredGlobalLabels {
		v, ok := labels[key]
		if !ok || v == "" {
			return fmt.Errorf("[global.labels] required label %q is missing or empty; please set it in config.toml", key)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Identity resolution with caching
// ---------------------------------------------------------------------------
//
// from_hostip / from_hostname support three resolution modes:
//   1. Fixed value (no "$")        → static, returned as-is.
//   2. Variable resolved from env  → static for process lifetime.
//   3. Variable with no env match  → auto-detected, cached for identCacheTTL.
//
// AgentIP / AgentHostname always return the freshest appropriate value.

const identCacheTTL = time.Minute

var (
	identMu sync.Mutex

	ipAutoDetect bool
	ipCached     string
	ipCachedAt   time.Time

	hostAutoDetect bool
	hostCached     string
	hostCachedAt   time.Time
)

// needsAutoDetect reports whether raw references ${varName} and the
// corresponding environment variable is empty (meaning we must auto-detect).
func needsAutoDetect(raw string, varName string) bool {
	if !strings.Contains(raw, "$") {
		return false
	}
	if !strings.Contains(raw, "${"+varName+"}") && !strings.Contains(raw, "$"+varName) {
		return false
	}
	return os.Getenv(varName) == ""
}

// AgentIP returns the IP identity exposed to the server.
// For auto-detected values (no env var), results are cached for 1 minute.
func AgentIP() string {
	if !ipAutoDetect {
		if Config != nil {
			if ip := strings.TrimSpace(Config.Global.Labels["from_hostip"]); ip != "" {
				return ip
			}
		}
		return DetectIP()
	}

	identMu.Lock()
	defer identMu.Unlock()
	if time.Since(ipCachedAt) < identCacheTTL && ipCached != "" {
		return ipCached
	}
	ipCached = DetectIP()
	ipCachedAt = time.Now()
	return ipCached
}

// AgentHostname returns the hostname identity exposed to the server.
// It follows the resolved from_hostname label when present.
// For auto-detected values (no env var), results are cached for 1 minute.
func AgentHostname() string {
	if !hostAutoDetect {
		if Config != nil {
			if h := strings.TrimSpace(Config.Global.Labels["from_hostname"]); h != "" {
				return h
			}
		}
		return DetectHostname()
	}

	identMu.Lock()
	defer identMu.Unlock()
	if time.Since(hostCachedAt) < identCacheTTL && hostCached != "" {
		return hostCached
	}
	hostCached = DetectHostname()
	hostCachedAt = time.Now()
	return hostCached
}

// AgentLabels returns a snapshot of global labels with live from_hostip and
// from_hostname values (reflecting any auto-detected changes).
func AgentLabels() map[string]string {
	if Config == nil {
		return nil
	}
	labels := make(map[string]string, len(Config.Global.Labels))
	for k, v := range Config.Global.Labels {
		labels[k] = v
	}
	labels["from_hostip"] = AgentIP()
	labels["from_hostname"] = AgentHostname()
	return labels
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
	if time.Duration(c.Gateway.RequestTimeout) == 0 {
		c.Gateway.RequestTimeout = Duration(30 * time.Second)
	}
	if c.Gateway.Enabled && c.Gateway.MaxRetries == 0 {
		c.Gateway.MaxRetries = 1
	}
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
		c.AggregateWindow = Duration(2 * time.Second)
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
		if m.MaxTokens <= 0 && m.MaxCompletionTokens <= 0 {
			m.MaxTokens = 4000
		}
		if m.ContextWindow <= 0 {
			m.ContextWindow = 128000
		}
		c.Models[name] = m
	}
}

// resolveAPIKeys resolves ${ENV_VAR} references in API keys and ExtraBody string values.
func (c *AIConfig) resolveAPIKeys() {
	c.Gateway.AgentToken = resolveEnvRef(c.Gateway.AgentToken)
	if c.Gateway.BaseURL != "" {
		c.Gateway.BaseURL = strings.TrimRight(c.Gateway.BaseURL, "/")
	}
	for name, m := range c.Models {
		m.APIKey = resolveEnvRef(m.APIKey)
		// Resolve ${ENV_VAR} in all string values of ExtraBody.
		for k, v := range m.ExtraBody {
			if s, ok := v.(string); ok {
				m.ExtraBody[k] = resolveEnvRef(s)
			}
		}
		// Bedrock: fallback to standard AWS env vars if not set in extra_body.
		if m.Provider == "bedrock" {
			if m.ExtraBody == nil {
				m.ExtraBody = make(map[string]interface{})
			}
			setExtraDefault(m.ExtraBody, "aws_access_key_id", "AWS_ACCESS_KEY_ID")
			setExtraDefault(m.ExtraBody, "aws_secret_access_key", "AWS_SECRET_ACCESS_KEY")
			setExtraDefault(m.ExtraBody, "aws_session_token", "AWS_SESSION_TOKEN")
		}
		c.Models[name] = m
	}
}

func resolveEnvRef(val string) string {
	if strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
		return os.Getenv(val[2 : len(val)-1])
	}
	return val
}

// setExtraDefault fills an ExtraBody key from an environment variable if not already set.
func setExtraDefault(extra map[string]interface{}, key, envVar string) {
	if v, ok := extra[key]; ok {
		if s, _ := v.(string); s != "" {
			return
		}
	}
	if val := os.Getenv(envVar); val != "" {
		extra[key] = val
	}
}

func (c *AIConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	hasLocalModels := len(c.Models) > 0
	requireLocalModels := !c.Gateway.Enabled || c.Gateway.FallbackToDirect || hasLocalModels
	if c.Gateway.Enabled {
		if c.Gateway.BaseURL == "" {
			return fmt.Errorf("[ai.gateway] base_url is required when enabled=true")
		}
		if c.Gateway.AgentToken == "" {
			return fmt.Errorf("[ai.gateway] agent_token is required when enabled=true (supports ${ENV_VAR} syntax)")
		}
		if c.Gateway.MaxRetries < 0 || c.Gateway.MaxRetries > 2 {
			return fmt.Errorf("[ai.gateway] max_retries must be between 0 and 2, got %d", c.Gateway.MaxRetries)
		}
	}
	if requireLocalModels {
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
			if m.Provider == "bedrock" {
				if m.Model == "" {
					return fmt.Errorf("[ai.models.%s] model is required for bedrock provider", name)
				}
				if m.ExtraStr("aws_region") == "" {
					return fmt.Errorf("[ai.models.%s] extra_body.aws_region is required for bedrock provider", name)
				}
			} else {
				if m.BaseURL == "" {
					return fmt.Errorf("[ai.models.%s] base_url is required", name)
				}
				if m.APIKey == "" {
					return fmt.Errorf("[ai.models.%s] api_key is required (supports ${ENV_VAR} syntax)", name)
				}
			}
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

func resolveGlobalLabels(labels map[string]string, builtins map[string]string) map[string]string {
	if raw, ok := labels["from_hostname"]; ok {
		resolved := resolveConfiguredHostname(raw, builtins)
		if resolved != "" {
			labels["from_hostname"] = resolved
			builtins["HOSTNAME"] = resolved
		}
		hostAutoDetect = needsAutoDetect(raw, "HOSTNAME")
		if hostAutoDetect {
			hostCached = resolved
			hostCachedAt = time.Now()
		}
	}

	if raw, ok := labels["from_hostip"]; ok {
		resolvedIP := resolveConfiguredHostIP(raw, builtins)
		if resolvedIP != "" {
			labels["from_hostip"] = resolvedIP
			builtins["IP"] = resolvedIP
		}
		ipAutoDetect = needsAutoDetect(raw, "IP")
		if ipAutoDetect {
			ipCached = resolvedIP
			ipCachedAt = time.Now()
		}
	}

	return expandLabels(labels, builtins)
}

func resolveConfiguredHostname(raw string, builtins map[string]string) string {
	if raw == "" || !strings.Contains(raw, "$") {
		return raw
	}

	var detected string
	return os.Expand(raw, func(key string) string {
		if key == "HOSTNAME" {
			if v := os.Getenv(key); v != "" {
				return v
			}
			if v, ok := builtins[key]; ok && v != "" {
				return v
			}
			if detected == "" {
				detected = DetectHostname()
			}
			return detected
		}
		if v, ok := builtins[key]; ok {
			return v
		}
		return os.Getenv(key)
	})
}

func resolveConfiguredHostIP(raw string, builtins map[string]string) string {
	if raw == "" || !strings.Contains(raw, "$") {
		return raw
	}

	var detectedIP string
	return os.Expand(raw, func(key string) string {
		if key == "IP" {
			if v := os.Getenv(key); v != "" {
				return v
			}
			if v, ok := builtins[key]; ok && v != "" {
				return v
			}
			if detectedIP == "" {
				detectedIP = DetectIP()
			}
			return detectedIP
		}
		if v, ok := builtins[key]; ok {
			return v
		}
		return os.Getenv(key)
	})
}
