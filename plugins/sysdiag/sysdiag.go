package sysdiag

import (
	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
)

func init() {
	plugins.AddDiagnoseRegistrar(func(registry *diagnose.ToolRegistry) {
		registerDmesg(registry)
		registerOOM(registry)
		registerIOTop(registry)
		registerCgroup(registry)
		registerDNS(registry)
		registerPing(registry)
		registerTraceroute(registry)
		registerLog(registry)
	})
}
