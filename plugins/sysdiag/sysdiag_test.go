package sysdiag

import (
	"testing"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

func TestAllToolsRegistered(t *testing.T) {
	registry := diagnose.NewToolRegistry()
	registerDmesg(registry)
	registerDNS(registry)
	registerPing(registry)
	registerTraceroute(registry)
	registerLog(registry)

	expected := []string{
		"dmesg_recent",
		"dns_resolve",
		"ping_check",
		"traceroute",
		"log_tail",
		"log_grep",
	}

	for _, name := range expected {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}
