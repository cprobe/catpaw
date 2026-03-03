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
		registerMount(registry)
		registerEnvInspect(registry)
		registerOpenFiles(registry)
		registerSS(registry)
		registerPSI(registry)
		registerInterrupts(registry)
		registerConntrackStat(registry)
		registerCoredump(registry)
		registerNUMA(registry)
		registerThermal(registry)
		registerLVM(registry)
		registerNetInterface(registry)
		registerIPAddr(registry)
		registerRoute(registry)
		registerBlockDevices(registry)
		registerARP(registry)
		registerFirewall(registry)
		registerSELinux(registry)
		registerThreads(registry)
		registerListen(registry)
		registerRetrans(registry)
		registerDiskLatency(registry)
		registerTCPTune(registry)
		registerConnLatency(registry)
		registerSoftnet(registry)
		registerDNS(registry)
		registerPing(registry)
		registerTraceroute(registry)
		registerLog(registry)
	})
}
