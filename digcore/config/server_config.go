package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ServerConfig defines the optional catpaw-server connection.
// When Enabled is false (default), the Agent runs in pure local mode.
type ServerConfig struct {
	Enabled         bool   `toml:"enabled"`
	Address         string `toml:"address"`
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
	if strings.HasPrefix(c.Address, "${") && strings.HasSuffix(c.Address, "}") {
		envKey := c.Address[2 : len(c.Address)-1]
		c.Address = os.Getenv(envKey)
	}
	if strings.HasPrefix(c.AgentToken, "${") && strings.HasSuffix(c.AgentToken, "}") {
		envKey := c.AgentToken[2 : len(c.AgentToken)-1]
		c.AgentToken = os.Getenv(envKey)
	}
	c.Address = strings.TrimRight(strings.TrimSpace(c.Address), "/")
}

func (c ServerConfig) HTTPBaseURL() (string, error) {
	return deriveServerBaseURL(c.Address)
}

func (c ServerConfig) WebSocketURL() (string, error) {
	baseURL, err := deriveServerBaseURL(c.Address)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse derived server base url: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported derived server base url scheme %q", parsed.Scheme)
	}
	parsed.Path = "/api/v1/agent/ws"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (c ServerConfig) GatewayBaseURL() (string, error) {
	baseURL, err := deriveServerBaseURL(c.Address)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse derived server base url: %w", err)
	}
	parsed.Path = "/api/v1/agent/llm"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func deriveServerBaseURL(address string) (string, error) {
	raw := strings.TrimSpace(address)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse server.address: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("server.address must be a host[:port] or absolute http(s) address")
	}
	switch parsed.Scheme {
	case "http", "https":
		// keep as-is
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	default:
		return "", fmt.Errorf("unsupported server.address scheme %q", parsed.Scheme)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("server.address must not include a path (got %q)", parsed.Path)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}
