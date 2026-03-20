package config

import (
	"os"
	"testing"
	"time"
)

func resetIdentState() {
	identMu.Lock()
	defer identMu.Unlock()
	ipAutoDetect = false
	ipCached = ""
	ipCachedAt = time.Time{}
	hostAutoDetect = false
	hostCached = ""
	hostCachedAt = time.Time{}
}

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

func TestResolveConfiguredHostname_UsesFixedValue(t *testing.T) {
	t.Setenv("HOSTNAME", "env-host")

	got := resolveConfiguredHostname("my-static-host", HostBuiltinsWithoutIP())
	if got != "my-static-host" {
		t.Fatalf("expected fixed hostname to be kept, got %q", got)
	}
}

func TestResolveConfiguredHostname_PrefersEnvHostname(t *testing.T) {
	t.Setenv("HOSTNAME", "env-host")

	got := resolveConfiguredHostname("${HOSTNAME}", HostBuiltinsWithoutIP())
	if got != "env-host" {
		t.Fatalf("expected env hostname, got %q", got)
	}
}

func TestResolveConfiguredHostname_FallsBackToBuiltins(t *testing.T) {
	t.Setenv("HOSTNAME", "")
	builtins := map[string]string{"HOSTNAME": "builtin-host"}

	got := resolveConfiguredHostname("${HOSTNAME}", builtins)
	if got != "builtin-host" {
		t.Fatalf("expected builtin hostname, got %q", got)
	}
}

func TestResolveGlobalLabels_UsesResolvedFromHostIPForIPBuiltins(t *testing.T) {
	t.Cleanup(resetIdentState)
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

func TestResolveGlobalLabels_HostnameFromEnvIsStatic(t *testing.T) {
	t.Cleanup(resetIdentState)
	t.Setenv("HOSTNAME", "env-host")

	labels := map[string]string{
		"from_hostname": "${HOSTNAME}",
		"from_hostip":   "10.10.0.21",
	}
	got := resolveGlobalLabels(labels, HostBuiltinsWithoutIP())
	if got["from_hostname"] != "env-host" {
		t.Fatalf("expected from_hostname = env-host, got %q", got["from_hostname"])
	}
	if hostAutoDetect {
		t.Fatal("expected hostAutoDetect=false when env var is set")
	}
}

func TestResolveGlobalLabels_HostnameAutoDetectWhenNoEnv(t *testing.T) {
	t.Cleanup(resetIdentState)
	t.Setenv("HOSTNAME", "")

	labels := map[string]string{
		"from_hostname": "${HOSTNAME}",
		"from_hostip":   "10.10.0.21",
	}
	resolveGlobalLabels(labels, HostBuiltinsWithoutIP())
	if !hostAutoDetect {
		t.Fatal("expected hostAutoDetect=true when env var is empty")
	}
}

func TestResolveGlobalLabels_IPAutoDetectWhenNoEnv(t *testing.T) {
	t.Cleanup(resetIdentState)
	t.Setenv("IP", "")

	labels := map[string]string{
		"from_hostname": "static-host",
		"from_hostip":   "${IP}",
	}
	resolveGlobalLabels(labels, HostBuiltinsWithoutIP())
	if !ipAutoDetect {
		t.Fatal("expected ipAutoDetect=true when env var is empty")
	}
}

func TestAgentIP_PrefersResolvedFromHostIP(t *testing.T) {
	t.Cleanup(resetIdentState)
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

func TestAgentHostname_PrefersResolvedFromHostname(t *testing.T) {
	t.Cleanup(resetIdentState)
	original := Config
	t.Cleanup(func() { Config = original })

	Config = &ConfigType{
		Global: Global{
			Labels: map[string]string{
				"from_hostname": "my-server",
			},
		},
	}

	if got := AgentHostname(); got != "my-server" {
		t.Fatalf("expected agent hostname from global label, got %q", got)
	}
}

func TestAgentHostname_AutoDetectUsesCaching(t *testing.T) {
	t.Cleanup(resetIdentState)
	original := Config
	t.Cleanup(func() { Config = original })

	hostAutoDetect = true
	hostCached = "cached-host"
	hostCachedAt = time.Now()

	Config = &ConfigType{
		Global: Global{
			Labels: map[string]string{
				"from_hostname": "stale-init-value",
			},
		},
	}

	if got := AgentHostname(); got != "cached-host" {
		t.Fatalf("expected cached hostname, got %q", got)
	}
}

func TestAgentIP_AutoDetectUsesCaching(t *testing.T) {
	t.Cleanup(resetIdentState)
	original := Config
	t.Cleanup(func() { Config = original })

	ipAutoDetect = true
	ipCached = "1.2.3.4"
	ipCachedAt = time.Now()

	Config = &ConfigType{
		Global: Global{
			Labels: map[string]string{
				"from_hostip": "stale-init-value",
			},
		},
	}

	if got := AgentIP(); got != "1.2.3.4" {
		t.Fatalf("expected cached ip, got %q", got)
	}
}

func TestAgentLabels_OverridesLiveValues(t *testing.T) {
	t.Cleanup(resetIdentState)
	original := Config
	t.Cleanup(func() { Config = original })

	Config = &ConfigType{
		Global: Global{
			Labels: map[string]string{
				"from_hostname": "my-host",
				"from_hostip":   "10.0.0.1",
			},
		},
	}

	labels := AgentLabels()
	if labels["from_hostname"] != "my-host" {
		t.Fatalf("expected from_hostname=my-host, got %q", labels["from_hostname"])
	}
	if labels["from_hostip"] != "10.0.0.1" {
		t.Fatalf("expected from_hostip=10.0.0.1, got %q", labels["from_hostip"])
	}
}

func TestNeedsAutoDetect(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		varName string
		envVal  string
		want    bool
	}{
		{"fixed value", "10.0.0.1", "IP", "", false},
		{"env var set", "${IP}", "IP", "1.2.3.4", false},
		{"env var empty", "${IP}", "IP", "", true},
		{"hostname env set", "${HOSTNAME}", "HOSTNAME", "web-01", false},
		{"hostname env empty", "${HOSTNAME}", "HOSTNAME", "", true},
		{"unrelated var", "${OTHER}", "IP", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.varName, tt.envVal)
			if got := needsAutoDetect(tt.raw, tt.varName); got != tt.want {
				t.Errorf("needsAutoDetect(%q, %q) = %v, want %v", tt.raw, tt.varName, got, tt.want)
			}
		})
	}
}
