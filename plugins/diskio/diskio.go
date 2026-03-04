package diskio

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/filter"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/shirou/gopsutil/v3/disk"
)

const pluginName = "diskio"

type IOLatencyCheck struct {
	Enabled    bool    `toml:"enabled"`
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
}

type Instance struct {
	config.InternalConfig

	Devices       []string `toml:"devices"`
	IgnoreDevices []string `toml:"ignore_devices"`

	IOLatency IOLatencyCheck `toml:"io_latency"`

	deviceFilter filter.Filter
	prevCounters map[string]disk.IOCountersStat
	prevTime     time.Time
	deviceTypes  map[string]string
	sampled      bool
	disabled     bool
}

type DiskIOPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &DiskIOPlugin{}
	})
}

func (p *DiskIOPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := range p.Instances {
		ret[i] = p.Instances[i]
	}
	return ret
}

func (ins *Instance) Init() error {
	if runtime.GOOS != "linux" {
		logger.Logger.Infow("diskio: io_latency check disabled, only supported on Linux",
			"os", runtime.GOOS)
		ins.disabled = true
		return nil
	}

	if !ins.IOLatency.Enabled {
		ins.disabled = true
		return nil
	}

	if ins.IOLatency.WarnGe > 0 && ins.IOLatency.CriticalGe > 0 &&
		ins.IOLatency.WarnGe >= ins.IOLatency.CriticalGe {
		return fmt.Errorf("diskio: io_latency.warn_ge(%.1f) must be less than io_latency.critical_ge(%.1f)",
			ins.IOLatency.WarnGe, ins.IOLatency.CriticalGe)
	}

	f, err := filter.NewIncludeExcludeFilter(ins.Devices, ins.IgnoreDevices)
	if err != nil {
		return fmt.Errorf("diskio: failed to compile device filter: %v", err)
	}
	ins.deviceFilter = f

	ins.prevCounters = make(map[string]disk.IOCountersStat)
	ins.deviceTypes = make(map[string]string)

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.disabled {
		return
	}

	counters, err := disk.IOCounters()
	if err != nil {
		logger.Logger.Errorw("diskio: failed to read IO counters", "error", err)
		q.PushFront(types.BuildEvent(map[string]string{
			"check":  "diskio::io_latency",
			"target": "all",
		}).SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to read IO counters: %v", err)))
		return
	}

	now := time.Now()

	if !ins.sampled {
		for name, c := range counters {
			if ins.shouldMonitor(name) {
				ins.prevCounters[name] = c
				ins.resolveDeviceType(name)
			}
		}
		ins.prevTime = now
		ins.sampled = true
		return
	}

	elapsed := now.Sub(ins.prevTime).Seconds()
	if elapsed <= 0 {
		ins.prevTime = now
		return
	}

	// Build the set of monitored devices in this round (single shouldMonitor call per device).
	monitored := make(map[string]disk.IOCountersStat, len(counters))
	for name, c := range counters {
		if ins.shouldMonitor(name) {
			monitored[name] = c
		}
	}

	for name, cur := range monitored {
		prev, ok := ins.prevCounters[name]
		if !ok {
			ins.resolveDeviceType(name)
			continue
		}
		ins.emitEvent(q, name, prev, cur, elapsed)
	}

	// Clean up devices that disappeared, then store current snapshot.
	for name := range ins.prevCounters {
		if _, exists := monitored[name]; !exists {
			delete(ins.prevCounters, name)
			delete(ins.deviceTypes, name)
		}
	}
	ins.prevCounters = monitored
	ins.prevTime = now
}

func (ins *Instance) emitEvent(q *safe.Queue[*types.Event], name string, prev, cur disk.IOCountersStat, elapsedSec float64) {
	dReads := safeDelta(cur.ReadCount, prev.ReadCount)
	dWrites := safeDelta(cur.WriteCount, prev.WriteCount)
	dReadMs := safeDelta(cur.ReadTime, prev.ReadTime)
	dWriteMs := safeDelta(cur.WriteTime, prev.WriteTime)
	dIOMs := safeDelta(cur.IoTime, prev.IoTime)

	totalIOs := dReads + dWrites
	totalMs := dReadMs + dWriteMs

	awaitMs := 0.0
	if totalIOs > 0 {
		awaitMs = float64(totalMs) / float64(totalIOs)
	}

	utilPct := float64(dIOMs) / (elapsedSec * 1000) * 100
	if utilPct > 100 {
		utilPct = 100
	}

	readIOPS := float64(dReads) / elapsedSec
	writeIOPS := float64(dWrites) / elapsedSec

	devType := ins.resolveDeviceType(name)
	warnGe, critGe := ins.thresholds(devType)

	event := types.BuildEvent(map[string]string{
		"check":  "diskio::io_latency",
		"target": name,
	})

	thresholdSrc := "auto"
	if ins.IOLatency.WarnGe > 0 || ins.IOLatency.CriticalGe > 0 {
		thresholdSrc = "manual"
	}

	var thresholdParts []string
	if warnGe > 0 {
		thresholdParts = append(thresholdParts, fmt.Sprintf("Warning >= %.1fms", warnGe))
	}
	if critGe > 0 {
		thresholdParts = append(thresholdParts, fmt.Sprintf("Critical >= %.1fms", critGe))
	}
	thresholdDesc := ""
	if len(thresholdParts) > 0 {
		thresholdDesc = strings.Join(thresholdParts, ", ") + " (" + devType + " " + thresholdSrc + ")"
	}

	attrs := map[string]string{
		"device_type":  devType,
		"await_ms":     fmt.Sprintf("%.1f", awaitMs),
		"util_percent": fmt.Sprintf("%.1f", utilPct),
		"read_iops":    fmt.Sprintf("%.1f", readIOPS),
		"write_iops":   fmt.Sprintf("%.1f", writeIOPS),
	}
	if thresholdDesc != "" {
		attrs[types.AttrThresholdDesc] = thresholdDesc
	}

	event.SetAttrs(attrs).SetCurrentValue(fmt.Sprintf("%.1fms", awaitMs))

	if totalIOs == 0 {
		q.PushFront(event.SetDescription(
			fmt.Sprintf("device %s (%s) idle, no IO during sampling interval", name, devType)))
		return
	}

	if critGe > 0 && awaitMs >= critGe {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).SetDescription(
			fmt.Sprintf("device %s (%s) await %.1fms >= critical threshold %.1fms (util %.1f%%, read %.1f IOPS, write %.1f IOPS)",
				name, devType, awaitMs, critGe, utilPct, readIOPS, writeIOPS)))
		return
	}

	if warnGe > 0 && awaitMs >= warnGe {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).SetDescription(
			fmt.Sprintf("device %s (%s) await %.1fms >= warning threshold %.1fms (util %.1f%%, read %.1f IOPS, write %.1f IOPS)",
				name, devType, awaitMs, warnGe, utilPct, readIOPS, writeIOPS)))
		return
	}

	q.PushFront(event.SetDescription(
		fmt.Sprintf("device %s (%s) await %.1fms, healthy (util %.1f%%, read %.1f IOPS, write %.1f IOPS)",
			name, devType, awaitMs, utilPct, readIOPS, writeIOPS)))
}

// thresholds returns (warn, critical) in ms. User overrides take precedence
// over the auto-detected device-type defaults.
func (ins *Instance) thresholds(devType string) (float64, float64) {
	warnGe := ins.IOLatency.WarnGe
	critGe := ins.IOLatency.CriticalGe

	if warnGe > 0 || critGe > 0 {
		return warnGe, critGe
	}

	switch devType {
	case "HDD":
		return 50, 200
	case "SSD":
		return 20, 100
	case "NVMe":
		return 10, 50
	default:
		return 100, 500
	}
}

// shouldMonitor returns true if the device should be monitored:
// not a virtual device, is a whole-disk block device, and passes user filter.
func (ins *Instance) shouldMonitor(name string) bool {
	if shouldSkipVirtual(name) {
		return false
	}

	if !isBlockDevice(name) {
		return false
	}

	if ins.deviceFilter != nil && !ins.deviceFilter.Match(name) {
		return false
	}

	return true
}

func (ins *Instance) resolveDeviceType(name string) string {
	if dt, ok := ins.deviceTypes[name]; ok {
		return dt
	}
	dt := detectDeviceType(name)
	ins.deviceTypes[name] = dt
	return dt
}

// shouldSkipVirtual returns true for virtual devices that should never be monitored.
func shouldSkipVirtual(name string) bool {
	return strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram")
}

// isBlockDevice checks /sys/block/<name> to determine if the device
// is a whole-disk block device (not a partition).
func isBlockDevice(name string) bool {
	_, err := os.Stat("/sys/block/" + name)
	return err == nil
}

func detectDeviceType(name string) string {
	if strings.HasPrefix(name, "nvme") {
		return "NVMe"
	}

	base := name
	for len(base) > 0 && base[len(base)-1] >= '0' && base[len(base)-1] <= '9' {
		base = base[:len(base)-1]
	}

	for _, candidate := range []string{name, base} {
		data, err := os.ReadFile("/sys/block/" + candidate + "/queue/rotational")
		if err != nil {
			continue
		}
		val := strings.TrimSpace(string(data))
		if val == "1" {
			return "HDD"
		}
		return "SSD"
	}
	return "?"
}

func safeDelta(v2, v1 uint64) uint64 {
	if v2 >= v1 {
		return v2 - v1
	}
	return 0
}
