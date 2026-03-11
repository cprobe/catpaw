package config

import (
	"os"
	"strings"
)

// ServerConfig defines the optional catpaw-server connection.
// When Enabled is false (default), the Agent runs in pure local mode.
type ServerConfig struct {
	Enabled         bool   `toml:"enabled"`
	URL             string `toml:"url"`
	AgentToken      string `toml:"agent_token"`
	CAFile          string `toml:"ca_file"`
	TLSSkipVerify   bool   `toml:"tls_skip_verify"`
	AlertBufferSize int    `toml:"alert_buffer_size"`
}

func (c *ServerConfig) GetAlertBufferSize() int {
	if c.AlertBufferSize <= 0 {
		return 1000
	}
	return c.AlertBufferSize
}

func (c *ServerConfig) resolve() {
	if strings.HasPrefix(c.AgentToken, "${") && strings.HasSuffix(c.AgentToken, "}") {
		envKey := c.AgentToken[2 : len(c.AgentToken)-1]
		c.AgentToken = os.Getenv(envKey)
	}
}
