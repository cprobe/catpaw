package config

import (
	"os"
	"strings"
	"testing"
)

func TestResolvedIdentity_ServerOverridesDefault(t *testing.T) {
	srv := MCPServerConfig{
		Identity: "custom-id",
	}
	got := srv.ResolvedIdentity("default-id")
	if got != "custom-id" {
		t.Errorf("expected custom-id, got %q", got)
	}
}

func TestResolvedIdentity_FallbackToDefault(t *testing.T) {
	srv := MCPServerConfig{}
	got := srv.ResolvedIdentity("default-id")
	if got != "default-id" {
		t.Errorf("expected default-id, got %q", got)
	}
}

func TestResolvedIdentity_AutoDetectWhenEmpty(t *testing.T) {
	srv := MCPServerConfig{}
	got := srv.ResolvedIdentity("")
	if got == "" {
		t.Error("expected auto-detected identity, got empty string")
	}
	if !strings.Contains(got, "hostname=") && !strings.Contains(got, "ip=") {
		t.Errorf("auto identity should contain hostname= or ip=, got %q", got)
	}
}

func TestExpandIdentityVars_Hostname(t *testing.T) {
	result := expandIdentityVars("host=${HOSTNAME}")
	hostname, _ := os.Hostname()
	expected := "host=" + hostname
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestExpandIdentityVars_EnvVar(t *testing.T) {
	os.Setenv("TEST_MCP_VAR", "test123")
	defer os.Unsetenv("TEST_MCP_VAR")

	result := expandIdentityVars("val=${TEST_MCP_VAR}")
	if result != "val=test123" {
		t.Errorf("expected val=test123, got %q", result)
	}
}

func TestExpandIdentityVars_ShortHostname(t *testing.T) {
	result := expandIdentityVars("short=${SHORT_HOSTNAME}")
	hostname, _ := os.Hostname()
	short := hostname
	if idx := strings.IndexByte(short, '.'); idx > 0 {
		short = short[:idx]
	}
	expected := "short=" + short
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestIsToolAllowed_EmptyWhitelist(t *testing.T) {
	srv := MCPServerConfig{}
	if !srv.IsToolAllowed("any_tool") {
		t.Error("empty whitelist should allow all tools")
	}
}

func TestIsToolAllowed_WithWhitelist(t *testing.T) {
	srv := MCPServerConfig{
		ToolsAllow: []string{"query", "query_range"},
	}
	if !srv.IsToolAllowed("query") {
		t.Error("query should be allowed")
	}
	if srv.IsToolAllowed("delete_data") {
		t.Error("delete_data should not be allowed")
	}
}
