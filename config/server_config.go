package config

import (
	"os"
	"strings"
)

// ServerConfig defines the optional catpaw-server connection.
// When Enabled is false (default), the Agent runs in pure local mode.
type ServerConfig struct {
	Enabled       bool   `toml:"enabled"`
	URL           string `toml:"url"`
	TenantToken   string `toml:"tenant_token"`
	CAFile        string `toml:"ca_file"`
	TLSSkipVerify bool   `toml:"tls_skip_verify"`
}

func (c *ServerConfig) resolve() {
	if strings.HasPrefix(c.TenantToken, "${") && strings.HasSuffix(c.TenantToken, "}") {
		envKey := c.TenantToken[2 : len(c.TenantToken)-1]
		c.TenantToken = os.Getenv(envKey)
	}
}
