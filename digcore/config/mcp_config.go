package config

import (
	"net"
	"os"
	"strings"
)

// MCPConfig holds configuration for MCP (Model Context Protocol) server connections.
type MCPConfig struct {
	Enabled         bool              `toml:"enabled"`
	DefaultIdentity string            `toml:"default_identity"`
	Servers         []MCPServerConfig `toml:"servers"`
}

// MCPServerConfig defines one MCP server connection.
type MCPServerConfig struct {
	Name       string            `toml:"name"`
	Command    string            `toml:"command"`
	Args       []string          `toml:"args"`
	Env        map[string]string `toml:"env"`
	Identity   string            `toml:"identity"`
	ToolsAllow []string          `toml:"tools_allow"`
}

// ResolvedIdentity returns the identity string for this server,
// falling back to defaultIdentity, then auto-detected host info.
// Variables ${HOSTNAME}, ${SHORT_HOSTNAME}, ${IP}, and ${ENV_VAR} are expanded.
// builtins should be obtained from HostBuiltins().
func (s *MCPServerConfig) ResolvedIdentity(defaultIdentity string, builtins map[string]string) string {
	raw := s.Identity
	if raw == "" {
		raw = defaultIdentity
	}
	if raw == "" {
		return autoIdentity(builtins)
	}
	return ExpandWithBuiltins(raw, builtins)
}

// IsToolAllowed checks whether a tool name passes the whitelist filter.
// If ToolsAllow is empty, all tools are allowed.
func (s *MCPServerConfig) IsToolAllowed(toolName string) bool {
	if len(s.ToolsAllow) == 0 {
		return true
	}
	for _, allowed := range s.ToolsAllow {
		if allowed == toolName {
			return true
		}
	}
	return false
}

// HostBuiltins computes host-level built-in variables once.
// Callers should compute this early and pass it to expandWithBuiltins.
func HostBuiltins() map[string]string {
	builtins := HostBuiltinsWithoutIP()
	builtins["IP"] = DetectIP()
	return builtins
}

// HostBuiltinsWithoutIP returns built-in host variables that do not require
// network probing.
func HostBuiltinsWithoutIP() map[string]string {
	hostname, _ := os.Hostname()
	short := hostname
	if idx := strings.IndexByte(short, '.'); idx > 0 {
		short = short[:idx]
	}
	return map[string]string{
		"HOSTNAME":       hostname,
		"SHORT_HOSTNAME": short,
	}
}

// ExpandWithBuiltins expands ${VAR} references in raw, checking builtins
// first and falling back to environment variables.
func ExpandWithBuiltins(raw string, builtins map[string]string) string {
	return os.Expand(raw, func(key string) string {
		if v, ok := builtins[key]; ok {
			return v
		}
		return os.Getenv(key)
	})
}

func autoIdentity(builtins map[string]string) string {
	parts := make([]string, 0, 2)
	if h := builtins["HOSTNAME"]; h != "" {
		parts = append(parts, "hostname="+h)
	}
	if ip := builtins["IP"]; ip != "" {
		parts = append(parts, "ip="+ip)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// DetectHostname returns the current system hostname via os.Hostname.
func DetectHostname() string {
	h, _ := os.Hostname()
	return h
}

// DetectIP returns the preferred outbound IPv4 address. It first tries
// gateway-based detection via GetOutboundIP, then falls back to scanning
// network interfaces.
func DetectIP() string {
	ip, err := GetOutboundIP()
	if err != nil {
		return detectIPFallback()
	}
	return ip.String()
}

func detectIPFallback() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return ""
}
