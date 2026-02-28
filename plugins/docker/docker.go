package docker

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/filter"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "docker"

type ContainerRunningCheck struct {
	TitleRule string `toml:"title_rule"`
}

type RestartDetectedCheck struct {
	Window     config.Duration `toml:"window"`
	WarnGe     int             `toml:"warn_ge"`
	CriticalGe int             `toml:"critical_ge"`
	TitleRule  string          `toml:"title_rule"`
}

type HealthStatusCheck struct {
	TitleRule string `toml:"title_rule"`
}

type CpuUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type MemoryUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type restartRecord struct {
	count     int
	timestamp time.Time
}

type containerRestartState struct {
	lastRestartCount int
	initialized      bool
	records          []restartRecord
}

type Instance struct {
	config.InternalConfig

	Socket        string          `toml:"socket"`
	APIVersion    string          `toml:"api_version"`
	Timeout       config.Duration `toml:"timeout"`
	Targets       []string        `toml:"targets"`
	Concurrency   int             `toml:"concurrency"`
	MaxContainers int             `toml:"max_containers"`

	ContainerRunning ContainerRunningCheck `toml:"container_running"`
	RestartDetected  RestartDetectedCheck  `toml:"restart_detected"`
	HealthStatus     HealthStatusCheck     `toml:"health_status"`
	CpuUsage         CpuUsageCheck         `toml:"cpu_usage"`
	MemoryUsage      MemoryUsageCheck      `toml:"memory_usage"`

	httpClient    *http.Client
	baseURL       string
	apiVersion    string
	explicitNames map[string]struct{}
	globFilter    filter.Filter
	restartStates map[string]*containerRestartState
}

type DockerPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *DockerPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &DockerPlugin{}
	})
}

func (ins *Instance) Init() error {
	if ins.Socket == "" {
		if runtime.GOOS == "windows" {
			ins.Socket = "http://localhost:2375"
		} else {
			ins.Socket = "/var/run/docker.sock"
		}
	}

	ins.Socket = strings.TrimRight(ins.Socket, "/")

	if ins.APIVersion != "" {
		ins.apiVersion = ins.APIVersion
	}

	if ins.Timeout <= 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}

	if ins.Concurrency <= 0 {
		ins.Concurrency = 5
	}

	if ins.MaxContainers <= 0 {
		ins.MaxContainers = 100
	}

	// restart_detected validation
	if time.Duration(ins.RestartDetected.Window) == 0 {
		ins.RestartDetected.Window = config.Duration(10 * time.Minute)
	}
	if time.Duration(ins.RestartDetected.Window) < time.Minute {
		return fmt.Errorf("restart_detected.window must be >= 1m")
	}
	if ins.RestartDetected.WarnGe > 0 && ins.RestartDetected.CriticalGe > 0 && ins.RestartDetected.WarnGe >= ins.RestartDetected.CriticalGe {
		return fmt.Errorf("restart_detected.warn_ge(%d) must be less than restart_detected.critical_ge(%d)",
			ins.RestartDetected.WarnGe, ins.RestartDetected.CriticalGe)
	}
	ins.restartStates = make(map[string]*containerRestartState)

	// cpu_usage validation
	if ins.CpuUsage.WarnGe > 0 && ins.CpuUsage.CriticalGe > 0 && ins.CpuUsage.WarnGe >= ins.CpuUsage.CriticalGe {
		return fmt.Errorf("cpu_usage.warn_ge(%.1f) must be less than cpu_usage.critical_ge(%.1f)",
			ins.CpuUsage.WarnGe, ins.CpuUsage.CriticalGe)
	}

	// memory_usage validation
	if ins.MemoryUsage.WarnGe > 0 && ins.MemoryUsage.CriticalGe > 0 && ins.MemoryUsage.WarnGe >= ins.MemoryUsage.CriticalGe {
		return fmt.Errorf("memory_usage.warn_ge(%.1f) must be less than memory_usage.critical_ge(%.1f)",
			ins.MemoryUsage.WarnGe, ins.MemoryUsage.CriticalGe)
	}

	// classify targets: explicit names vs glob patterns
	ins.explicitNames = make(map[string]struct{})
	var globs []string
	for _, t := range ins.Targets {
		if filter.HasMeta(t) {
			globs = append(globs, t)
		} else {
			ins.explicitNames[t] = struct{}{}
		}
	}
	if len(globs) > 0 {
		var err error
		ins.globFilter, err = filter.Compile(globs)
		if err != nil {
			return fmt.Errorf("invalid glob pattern in targets: %v", err)
		}
	}

	// build HTTP client
	ins.httpClient, ins.baseURL = newDockerHTTPClient(ins.Socket, time.Duration(ins.Timeout))

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.Targets) == 0 {
		return
	}

	// Negotiate API version on first Gather if not configured
	if ins.apiVersion == "" {
		ins.negotiateAPIVersion()
	}

	containers, err := ins.listContainers()
	if err != nil {
		q.PushFront(ins.buildEvent("docker::container_running", "docker-engine", ins.ContainerRunning.TitleRule).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to list containers: %v", err)))
		return
	}

	q.PushFront(ins.buildEvent("docker::container_running", "docker-engine", ins.ContainerRunning.TitleRule).
		SetDescription(fmt.Sprintf("Docker Engine is reachable, %d containers found", len(containers))))

	matched := ins.matchTargets(containers)

	if len(matched) > ins.MaxContainers {
		q.PushFront(ins.buildEvent("docker::container_running", "docker-engine", ins.ContainerRunning.TitleRule).
			SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("matched %d containers, exceeding max_containers %d, only checking first %d",
				len(matched), ins.MaxContainers, ins.MaxContainers)))
		matched = matched[:ins.MaxContainers]
	}

	// Explicit names not found → Critical
	matchedNames := make(map[string]struct{}, len(matched))
	for _, c := range matched {
		matchedNames[containerName(c)] = struct{}{}
	}
	for name := range ins.explicitNames {
		if _, ok := matchedNames[name]; !ok {
			q.PushFront(ins.buildEvent("docker::container_running", name, ins.ContainerRunning.TitleRule).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("container %q not found", name)))
		}
	}

	needStats := ins.CpuUsage.WarnGe > 0 || ins.CpuUsage.CriticalGe > 0 ||
		ins.MemoryUsage.WarnGe > 0 || ins.MemoryUsage.CriticalGe > 0

	// Pre-fetch restartStates pointers (Go map not concurrent-safe)
	type containerWithState struct {
		entry containerListEntry
		state *containerRestartState
	}
	items := make([]containerWithState, 0, len(matched))
	for _, c := range matched {
		name := containerName(c)
		if _, ok := ins.restartStates[name]; !ok {
			ins.restartStates[name] = &containerRestartState{}
		}
		items = append(items, containerWithState{entry: c, state: ins.restartStates[name]})
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)

	for _, item := range items {
		wg.Add(1)
		go func(c containerListEntry, state *containerRestartState) {
			se.Acquire()
			defer func() {
				if r := recover(); r != nil {
					name := containerName(c)
					logger.Logger.Errorw("panic in docker gather goroutine", "container", name, "recover", r)
					q.PushFront(ins.buildEvent("docker::panic", name, "[check] [target]").
						SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				se.Release()
				wg.Done()
			}()

			name := containerName(c)
			shortID := shortContainerID(c.Id)
			image := c.Image

			ins.checkContainerRunning(q, c, name, shortID)

			detail, err := ins.inspectContainer(c.Id)
			if err != nil {
				q.PushFront(ins.buildContainerEvent("docker::container_running", name, shortID, image, ins.ContainerRunning.TitleRule).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to inspect container %q: %v", name, err)))
				return
			}

			ins.checkRestartDetected(q, detail, name, shortID, image, state)

			if c.State != "running" {
				return
			}

			ins.checkHealthStatus(q, detail, name, shortID, image)

			if needStats {
				stats, err := ins.getContainerStats(c.Id)
				if err != nil {
					if ins.CpuUsage.WarnGe > 0 || ins.CpuUsage.CriticalGe > 0 {
						q.PushFront(ins.buildContainerEvent("docker::cpu_usage", name, shortID, image, ins.CpuUsage.TitleRule).
							SetEventStatus(types.EventStatusCritical).
							SetDescription(fmt.Sprintf("failed to get container stats for %q: %v", name, err)))
					}
					if ins.MemoryUsage.WarnGe > 0 || ins.MemoryUsage.CriticalGe > 0 {
						q.PushFront(ins.buildContainerEvent("docker::memory_usage", name, shortID, image, ins.MemoryUsage.TitleRule).
							SetEventStatus(types.EventStatusCritical).
							SetDescription(fmt.Sprintf("failed to get container stats for %q: %v", name, err)))
					}
				} else {
					ins.checkCpuUsage(q, stats, detail, name, shortID, image)
					ins.checkMemoryUsage(q, stats, name, shortID, image)
				}
			}
		}(item.entry, item.state)
	}

	wg.Wait()

	// Cleanup stale restartStates for removed containers
	currentNames := make(map[string]struct{}, len(containers))
	for _, c := range containers {
		currentNames[containerName(c)] = struct{}{}
	}
	for name := range ins.restartStates {
		if _, ok := currentNames[name]; !ok {
			delete(ins.restartStates, name)
		}
	}
}

// matchTargets returns containers that match any of the configured targets.
// Explicit names match against all containers; glob patterns match only active containers.
func (ins *Instance) matchTargets(containers []containerListEntry) []containerListEntry {
	seen := make(map[string]struct{})
	var matched []containerListEntry

	for _, c := range containers {
		name := containerName(c)

		if _, ok := ins.explicitNames[name]; ok {
			if _, dup := seen[name]; !dup {
				matched = append(matched, c)
				seen[name] = struct{}{}
			}
			continue
		}

		if ins.globFilter != nil && isActiveState(c.State) && ins.globFilter.Match(name) {
			if _, dup := seen[name]; !dup {
				matched = append(matched, c)
				seen[name] = struct{}{}
			}
		}
	}

	return matched
}

func isActiveState(state string) bool {
	switch state {
	case "running", "paused", "restarting":
		return true
	}
	return false
}

// --- Check functions ---

func (ins *Instance) checkContainerRunning(q *safe.Queue[*types.Event], c containerListEntry, name, shortID string) {
	event := ins.buildContainerEvent("docker::container_running", name, shortID, c.Image, ins.ContainerRunning.TitleRule)
	event.Labels[types.AttrPrefix+"state"] = c.State
	event.Labels[types.AttrPrefix+"status"] = c.Status

	switch c.State {
	case "running":
		event.SetDescription(fmt.Sprintf("container %q is running", name))
	case "paused":
		event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("container %q is paused", name))
	case "restarting":
		event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("container %q is restarting", name))
	default:
		desc := fmt.Sprintf("container %q is not running (state: %s", name, c.State)
		// Try to extract exit code from Status string for list-level info
		if c.State == "exited" || c.State == "dead" {
			desc += fmt.Sprintf(", status: %s", c.Status)
		}
		desc += ")"
		event.SetEventStatus(types.EventStatusCritical).SetDescription(desc)
	}

	q.PushFront(event)
}

func (ins *Instance) checkRestartDetected(q *safe.Queue[*types.Event], detail *containerInspect, name, shortID, image string, state *containerRestartState) {
	warnGe := ins.RestartDetected.WarnGe
	criticalGe := ins.RestartDetected.CriticalGe
	if warnGe <= 0 && criticalGe <= 0 {
		return
	}

	window := time.Duration(ins.RestartDetected.Window)
	currentCount := detail.RestartCount
	now := time.Now()

	if !state.initialized {
		state.lastRestartCount = currentCount
		state.initialized = true
	} else {
		delta := currentCount - state.lastRestartCount
		if delta < 0 {
			// Container recreated, RestartCount reset
			state.lastRestartCount = currentCount
			state.records = nil
		} else if delta > 0 {
			state.records = append(state.records, restartRecord{count: delta, timestamp: now})
			state.lastRestartCount = currentCount
		}
	}

	// Expire old records outside window
	cutoff := now.Add(-window)
	validIdx := 0
	for _, r := range state.records {
		if r.timestamp.After(cutoff) {
			state.records[validIdx] = r
			validIdx++
		}
	}
	state.records = state.records[:validIdx]

	restartsInWindow := 0
	for _, r := range state.records {
		restartsInWindow += r.count
	}

	event := ins.buildContainerEvent("docker::restart_detected", name, shortID, image, ins.RestartDetected.TitleRule)
	event.Labels[types.AttrPrefix+"restarts_in_window"] = strconv.Itoa(restartsInWindow)
	event.Labels[types.AttrPrefix+"window"] = humanDuration(window)
	event.Labels[types.AttrPrefix+"restart_count"] = strconv.Itoa(currentCount)

	if detail.State.OOMKilled {
		event.Labels[types.AttrPrefix+"oom_killed"] = "true"
	}
	event.Labels[types.AttrPrefix+"exit_code"] = strconv.Itoa(detail.State.ExitCode)

	if t := parseDockerTime(detail.State.StartedAt); !t.IsZero() {
		event.Labels[types.AttrPrefix+"started_at"] = t.Local().Format("2006-01-02 15:04:05 MST")
	}
	if t := parseDockerTime(detail.State.FinishedAt); !t.IsZero() {
		event.Labels[types.AttrPrefix+"finished_at"] = t.Local().Format("2006-01-02 15:04:05 MST")
	}

	if criticalGe > 0 && restartsInWindow >= criticalGe {
		desc := fmt.Sprintf("container %q restarted %d times in last %s, above critical threshold %d",
			name, restartsInWindow, humanDuration(window), criticalGe)
		if detail.State.OOMKilled {
			desc += fmt.Sprintf(" (OOM killed, exit code: %d)", detail.State.ExitCode)
		}
		event.SetEventStatus(types.EventStatusCritical).SetDescription(desc)
	} else if warnGe > 0 && restartsInWindow >= warnGe {
		desc := fmt.Sprintf("container %q restarted %d times in last %s, above warning threshold %d",
			name, restartsInWindow, humanDuration(window), warnGe)
		if detail.State.OOMKilled {
			desc += fmt.Sprintf(" (OOM killed, exit code: %d)", detail.State.ExitCode)
		}
		event.SetEventStatus(types.EventStatusWarning).SetDescription(desc)
	} else {
		event.SetDescription(fmt.Sprintf("container %q restarted %d times in last %s",
			name, restartsInWindow, humanDuration(window)))
	}

	q.PushFront(event)
}

func (ins *Instance) checkHealthStatus(q *safe.Queue[*types.Event], detail *containerInspect, name, shortID, image string) {
	if detail.State.Health == nil {
		return
	}

	event := ins.buildContainerEvent("docker::health_status", name, shortID, image, ins.HealthStatus.TitleRule)
	health := detail.State.Health

	event.Labels[types.AttrPrefix+"health_status"] = health.Status

	switch health.Status {
	case "healthy":
		event.SetDescription(fmt.Sprintf("container %q health check: healthy", name))
	case "starting":
		event.SetDescription(fmt.Sprintf("container %q health check: starting", name))
	case "unhealthy":
		event.Labels[types.AttrPrefix+"health_failing_streak"] = strconv.Itoa(health.FailingStreak)
		if len(health.Log) > 0 {
			lastOutput := health.Log[len(health.Log)-1].Output
			event.Labels[types.AttrPrefix+"health_last_output"] = truncate(strings.TrimSpace(lastOutput), 200)
		}
		event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("container %q health check: unhealthy (failing streak: %d)", name, health.FailingStreak))
	default:
		event.SetDescription(fmt.Sprintf("container %q health check: %s", name, health.Status))
	}

	q.PushFront(event)
}

func (ins *Instance) checkCpuUsage(q *safe.Queue[*types.Event], stats *containerStats, detail *containerInspect, name, shortID, image string) {
	if ins.CpuUsage.WarnGe <= 0 && ins.CpuUsage.CriticalGe <= 0 {
		return
	}

	cpuDelta := stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage
	systemDelta := stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage

	onlineCPUs := stats.CPUStats.OnlineCPUs
	if onlineCPUs == 0 {
		onlineCPUs = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}

	if systemDelta == 0 || onlineCPUs == 0 {
		return
	}

	cpuCores := float64(cpuDelta) / float64(systemDelta) * float64(onlineCPUs)

	allocatedCPUs := getAllocatedCPUs(detail.HostConfig)

	var cpuPercent float64
	var cpuLimitStr string
	if allocatedCPUs > 0 {
		cpuPercent = cpuCores / allocatedCPUs * 100
		cpuLimitStr = fmt.Sprintf("%.1f cores", allocatedCPUs)
	} else {
		cpuPercent = cpuCores / float64(onlineCPUs) * 100
		cpuLimitStr = fmt.Sprintf("%d cores (unlimited)", onlineCPUs)
	}

	event := ins.buildContainerEvent("docker::cpu_usage", name, shortID, image, ins.CpuUsage.TitleRule)
	event.Labels[types.AttrPrefix+"cpu_percent"] = fmt.Sprintf("%.1f%%", cpuPercent)
	event.Labels[types.AttrPrefix+"cpu_limit"] = cpuLimitStr
	event.Labels[types.AttrPrefix+"online_cpus"] = strconv.FormatUint(uint64(onlineCPUs), 10)

	td := stats.CPUStats.ThrottlingData
	if td.ThrottledPeriods > 0 || td.ThrottledTime > 0 {
		event.Labels[types.AttrPrefix+"cpu_throttled_periods"] = strconv.FormatUint(td.ThrottledPeriods, 10)
		throttledSec := float64(td.ThrottledTime) / 1e9
		event.Labels[types.AttrPrefix+"cpu_throttled_time"] = fmt.Sprintf("%.1fs", throttledSec)
	}

	status := types.EvaluateGeThreshold(cpuPercent, ins.CpuUsage.WarnGe, ins.CpuUsage.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("container %q CPU usage %.1f%% of %s, above critical threshold %.0f%%",
			name, cpuPercent, cpuLimitStr, ins.CpuUsage.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("container %q CPU usage %.1f%% of %s, above warning threshold %.0f%%",
			name, cpuPercent, cpuLimitStr, ins.CpuUsage.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("container %q CPU usage %.1f%% of %s",
			name, cpuPercent, cpuLimitStr))
	}

	q.PushFront(event)
}

func (ins *Instance) checkMemoryUsage(q *safe.Queue[*types.Event], stats *containerStats, name, shortID, image string) {
	if ins.MemoryUsage.WarnGe <= 0 && ins.MemoryUsage.CriticalGe <= 0 {
		return
	}

	limit := stats.MemoryStats.Limit
	if limit == 0 || limit > 1<<50 {
		return
	}

	fileCache := getFileCache(stats.MemoryStats)
	actualUsage := stats.MemoryStats.Usage
	if actualUsage > fileCache {
		actualUsage -= fileCache
	} else {
		actualUsage = 0
	}

	memPercent := float64(actualUsage) / float64(limit) * 100

	usedStr := humanBytes(actualUsage)
	limitStr := humanBytes(limit)

	event := ins.buildContainerEvent("docker::memory_usage", name, shortID, image, ins.MemoryUsage.TitleRule)
	event.Labels[types.AttrPrefix+"memory_percent"] = fmt.Sprintf("%.1f%%", memPercent)
	event.Labels[types.AttrPrefix+"memory_used"] = usedStr
	event.Labels[types.AttrPrefix+"memory_limit"] = limitStr

	status := types.EvaluateGeThreshold(memPercent, ins.MemoryUsage.WarnGe, ins.MemoryUsage.CriticalGe)
	event.SetEventStatus(status)

	switch status {
	case types.EventStatusCritical:
		event.SetDescription(fmt.Sprintf("container %q memory usage %.1f%% (%s / %s), above critical threshold %.0f%%",
			name, memPercent, usedStr, limitStr, ins.MemoryUsage.CriticalGe))
	case types.EventStatusWarning:
		event.SetDescription(fmt.Sprintf("container %q memory usage %.1f%% (%s / %s), above warning threshold %.0f%%",
			name, memPercent, usedStr, limitStr, ins.MemoryUsage.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("container %q memory usage %.1f%% (%s / %s)",
			name, memPercent, usedStr, limitStr))
	}

	q.PushFront(event)
}

// --- Event builders ---

func (ins *Instance) buildEvent(check, target, titleRule string) *types.Event {
	if titleRule == "" {
		titleRule = "[check] [target]"
	}
	return types.BuildEvent(map[string]string{
		"check":  check,
		"target": target,
	}).SetTitleRule(titleRule)
}

func (ins *Instance) buildContainerEvent(check, name, shortID, image, titleRule string) *types.Event {
	if titleRule == "" {
		titleRule = "[check] [target]"
	}
	labels := map[string]string{
		"check":  check,
		"target": name,
	}
	if shortID != "" {
		labels[types.AttrPrefix+"container_id"] = shortID
	}
	if image != "" {
		labels[types.AttrPrefix+"container_image"] = image
	}
	return types.BuildEvent(labels).SetTitleRule(titleRule)
}

// --- Helpers ---

func containerName(c containerListEntry) string {
	if len(c.Names) == 0 {
		return shortContainerID(c.Id)
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func getAllocatedCPUs(hc containerHostConfig) float64 {
	if hc.NanoCPUs > 0 {
		return float64(hc.NanoCPUs) / 1e9
	}
	if hc.CpuQuota > 0 && hc.CpuPeriod > 0 {
		return float64(hc.CpuQuota) / float64(hc.CpuPeriod)
	}
	return 0
}

func getFileCache(m memoryStats) uint64 {
	if v := getStatUint64(m.Stats, "inactive_file"); v > 0 {
		return v
	}
	if v := getStatUint64(m.Stats, "total_inactive_file"); v > 0 {
		return v
	}
	if v := getStatUint64(m.Stats, "cache"); v > 0 {
		return v
	}
	return 0
}

func getStatUint64(stats map[string]interface{}, key string) uint64 {
	v, ok := stats[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return uint64(n)
	case json.Number:
		i, _ := n.Int64()
		return uint64(i)
	}
	return 0
}

func parseDockerTime(s string) time.Time {
	if s == "" || s == "0001-01-01T00:00:00Z" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}

	totalSec := int(d.Seconds())
	days := totalSec / 86400
	hours := (totalSec % 86400) / 3600
	minutes := (totalSec % 3600) / 60
	seconds := totalSec % 60

	if days > 0 {
		s := fmt.Sprintf("%dd", days)
		if hours > 0 {
			s += fmt.Sprintf(" %dh", hours)
		}
		if minutes > 0 {
			s += fmt.Sprintf(" %dm", minutes)
		}
		return s
	}
	if hours > 0 {
		s := fmt.Sprintf("%dh", hours)
		if minutes > 0 {
			s += fmt.Sprintf(" %dm", minutes)
		}
		if seconds > 0 {
			s += fmt.Sprintf(" %ds", seconds)
		}
		return s
	}
	if minutes > 0 {
		s := fmt.Sprintf("%dm", minutes)
		if seconds > 0 {
			s += fmt.Sprintf(" %ds", seconds)
		}
		return s
	}
	return fmt.Sprintf("%ds", seconds)
}

func humanBytes(b uint64) string {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", math.Round(float64(b)/float64(gib)*10)/10)
	case b >= mib:
		return fmt.Sprintf("%.0f MiB", math.Round(float64(b)/float64(mib)))
	case b >= kib:
		return fmt.Sprintf("%.0f KiB", math.Round(float64(b)/float64(kib)))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
