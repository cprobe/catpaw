package config

import (
	"os"
	"testing"
)

func TestResolveConfiguredHostIP_UsesFixedValue(t *testing.T) {
	t.Setenv("IP", "192.168.10.20")

	got := resolveConfiguredHostIP("10.10.0.21", HostBuiltinsWithoutIP())
	if got != "10.10.0.21" {
		t.Fatalf("expected fixed ip to be kept, got %q", got)
	}
}

func TestResolveConfiguredHostIP_PrefersEnvIP(t *testing.T) {
	t.Setenv("IP", "192.168.10.20")

	got := resolveConfiguredHostIP("${IP}", HostBuiltinsWithoutIP())
	if got != "192.168.10.20" {
		t.Fatalf("expected env ip, got %q", got)
	}
}

func TestResolveGlobalLabels_UsesResolvedFromHostIPForIPBuiltins(t *testing.T) {
	hostname, _ := os.Hostname()
	labels := map[string]string{
		"from_hostname": "${HOSTNAME}",
		"from_hostip":   "10.10.0.21",
		"peer_ip":       "${IP}",
	}

	got := resolveGlobalLabels(labels, HostBuiltinsWithoutIP())
	if got["from_hostip"] != "10.10.0.21" {
		t.Fatalf("expected from_hostip to stay fixed, got %q", got["from_hostip"])
	}
	if got["peer_ip"] != "10.10.0.21" {
		t.Fatalf("expected peer_ip to reuse resolved from_hostip, got %q", got["peer_ip"])
	}
	if got["from_hostname"] != hostname {
		t.Fatalf("expected hostname %q, got %q", hostname, got["from_hostname"])
	}
}

func TestAgentIP_PrefersResolvedFromHostIP(t *testing.T) {
	original := Config
	t.Cleanup(func() { Config = original })

	Config = &ConfigType{
		Global: Global{
			Labels: map[string]string{
				"from_hostip": "10.10.0.21",
			},
		},
	}

	if got := AgentIP(); got != "10.10.0.21" {
		t.Fatalf("expected agent ip from global label, got %q", got)
	}
}
