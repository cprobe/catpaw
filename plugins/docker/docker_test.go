package docker

import (
	"testing"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
)

func popEvent(q *safe.Queue[*types.Event]) *types.Event {
	all := q.PopBackAll()
	if len(all) == 0 {
		return nil
	}
	return all[0]
}

func TestInit_Defaults(t *testing.T) {
	ins := &Instance{
		Targets: []string{"nginx"},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if ins.Concurrency != 5 {
		t.Errorf("expected concurrency 5, got %d", ins.Concurrency)
	}
	if ins.MaxContainers != 100 {
		t.Errorf("expected max_containers 100, got %d", ins.MaxContainers)
	}
	if time.Duration(ins.Timeout) != 10*time.Second {
		t.Errorf("expected timeout 10s, got %s", time.Duration(ins.Timeout))
	}
	if time.Duration(ins.RestartDetected.Window) != 10*time.Minute {
		t.Errorf("expected window 10m, got %s", time.Duration(ins.RestartDetected.Window))
	}
	if _, ok := ins.explicitNames["nginx"]; !ok {
		t.Error("expected 'nginx' in explicitNames")
	}
}

func TestInit_EmptyTargets(t *testing.T) {
	ins := &Instance{}
	if err := ins.Init(); err != nil {
		t.Errorf("empty targets should not cause Init error, got: %v", err)
	}
}

func TestInit_RestartDetectedValidation(t *testing.T) {
	ins := &Instance{
		Targets: []string{"*"},
		RestartDetected: RestartDetectedCheck{
			WarnGe:     5,
			CriticalGe: 3,
		},
	}
	if err := ins.Init(); err == nil {
		t.Error("expected error: warn_ge >= critical_ge")
	}
}

func TestInit_WindowTooSmall(t *testing.T) {
	ins := &Instance{
		Targets: []string{"*"},
		RestartDetected: RestartDetectedCheck{
			Window: config.Duration(30 * time.Second),
		},
	}
	if err := ins.Init(); err == nil {
		t.Error("expected error: window < 1m")
	}
}

func TestInit_CpuUsageValidation(t *testing.T) {
	ins := &Instance{
		Targets: []string{"*"},
		CpuUsage: CpuUsageCheck{
			WarnGe:     90,
			CriticalGe: 80,
		},
	}
	if err := ins.Init(); err == nil {
		t.Error("expected error: warn_ge >= critical_ge")
	}
}

func TestInit_MemoryUsageValidation(t *testing.T) {
	ins := &Instance{
		Targets: []string{"*"},
		MemoryUsage: MemoryUsageCheck{
			WarnGe:     95,
			CriticalGe: 80,
		},
	}
	if err := ins.Init(); err == nil {
		t.Error("expected error: warn_ge >= critical_ge")
	}
}

func TestInit_ExplicitAndGlob(t *testing.T) {
	ins := &Instance{
		Targets: []string{"nginx", "redis", "app-*"},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if len(ins.explicitNames) != 2 {
		t.Errorf("expected 2 explicit names, got %d", len(ins.explicitNames))
	}
	if _, ok := ins.explicitNames["nginx"]; !ok {
		t.Error("expected 'nginx' in explicitNames")
	}
	if _, ok := ins.explicitNames["redis"]; !ok {
		t.Error("expected 'redis' in explicitNames")
	}
}

func TestMatchTargets(t *testing.T) {
	ins := &Instance{
		Targets: []string{"nginx", "app-*"},
	}
	_ = ins.Init()

	containers := []containerListEntry{
		{Id: "aaa", Names: []string{"/nginx"}, State: "running"},
		{Id: "bbb", Names: []string{"/redis"}, State: "running"},
		{Id: "ccc", Names: []string{"/app-web"}, State: "running"},
		{Id: "ddd", Names: []string{"/app-worker"}, State: "exited"},
		{Id: "eee", Names: []string{"/app-api"}, State: "paused"},
	}

	matched := ins.matchTargets(containers)

	names := make(map[string]bool)
	for _, c := range matched {
		names[containerName(c)] = true
	}

	if !names["nginx"] {
		t.Error("expected nginx to match (explicit)")
	}
	if names["redis"] {
		t.Error("redis should not match")
	}
	if !names["app-web"] {
		t.Error("expected app-web to match (glob, running)")
	}
	if names["app-worker"] {
		t.Error("app-worker should not match (glob, exited)")
	}
	if !names["app-api"] {
		t.Error("expected app-api to match (glob, paused = active)")
	}
}

func TestCheckContainerRunning(t *testing.T) {
	ins := &Instance{Targets: []string{"*"}}
	_ = ins.Init()

	tests := []struct {
		state    string
		expected string
	}{
		{"running", types.EventStatusOk},
		{"paused", types.EventStatusWarning},
		{"restarting", types.EventStatusWarning},
		{"exited", types.EventStatusCritical},
		{"dead", types.EventStatusCritical},
		{"created", types.EventStatusCritical},
	}

	for _, tt := range tests {
		q := safe.NewQueue[*types.Event]()
		c := containerListEntry{Id: "abc123def456", Names: []string{"/test"}, State: tt.state, Status: "some status"}
		ins.checkContainerRunning(q, c, "test", "abc123def456")

		event := popEvent(q)
		if event == nil {
			t.Errorf("state=%s: no event produced", tt.state)
			continue
		}
		if event.EventStatus != tt.expected {
			t.Errorf("state=%s: expected %s, got %s", tt.state, tt.expected, event.EventStatus)
		}
	}
}

func TestCheckRestartDetected_SlidingWindow(t *testing.T) {
	ins := &Instance{
		Targets: []string{"*"},
		RestartDetected: RestartDetectedCheck{
			Window:     config.Duration(10 * time.Minute),
			WarnGe:     3,
			CriticalGe: 5,
		},
	}
	_ = ins.Init()

	state := &containerRestartState{}

	// First gather: baseline (RestartCount=2)
	q := safe.NewQueue[*types.Event]()
	detail := &containerInspect{RestartCount: 2}
	ins.checkRestartDetected(q, detail, "app", "abc123", "nginx:1.25", state)
	event := popEvent(q)
	if event == nil {
		t.Fatal("expected event for first gather")
	}
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("first gather: expected Ok, got %s", event.EventStatus)
	}

	// Second gather: RestartCount jumped to 5 (delta=3, within window)
	q = safe.NewQueue[*types.Event]()
	detail = &containerInspect{RestartCount: 5}
	ins.checkRestartDetected(q, detail, "app", "abc123", "nginx:1.25", state)
	event = popEvent(q)
	if event == nil {
		t.Fatal("expected event for second gather")
	}
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("second gather: expected Warning (3 restarts), got %s: %s", event.EventStatus, event.Description)
	}

	// Third gather: RestartCount jumped to 8 (delta=3, total in window=6)
	q = safe.NewQueue[*types.Event]()
	detail = &containerInspect{RestartCount: 8}
	ins.checkRestartDetected(q, detail, "app", "abc123", "nginx:1.25", state)
	event = popEvent(q)
	if event == nil {
		t.Fatal("expected event for third gather")
	}
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("third gather: expected Critical (6 restarts), got %s: %s", event.EventStatus, event.Description)
	}
}

func TestCheckRestartDetected_WindowExpiry(t *testing.T) {
	ins := &Instance{
		Targets: []string{"*"},
		RestartDetected: RestartDetectedCheck{
			Window:     config.Duration(10 * time.Minute),
			WarnGe:     3,
			CriticalGe: 5,
		},
	}
	_ = ins.Init()

	state := &containerRestartState{
		initialized:      true,
		lastRestartCount: 0,
		records: []restartRecord{
			{count: 3, timestamp: time.Now().Add(-15 * time.Minute)},
		},
	}

	q := safe.NewQueue[*types.Event]()
	detail := &containerInspect{RestartCount: 0}
	ins.checkRestartDetected(q, detail, "app", "abc123", "nginx:1.25", state)
	event := popEvent(q)
	if event == nil {
		t.Fatal("expected event")
	}
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok after window expiry, got %s", event.EventStatus)
	}
}

func TestCheckRestartDetected_ContainerRecreated(t *testing.T) {
	ins := &Instance{
		Targets: []string{"*"},
		RestartDetected: RestartDetectedCheck{
			Window:     config.Duration(10 * time.Minute),
			WarnGe:     3,
			CriticalGe: 5,
		},
	}
	_ = ins.Init()

	state := &containerRestartState{
		initialized:      true,
		lastRestartCount: 10,
		records: []restartRecord{
			{count: 4, timestamp: time.Now()},
		},
	}

	// RestartCount dropped from 10 to 1 → container recreated
	q := safe.NewQueue[*types.Event]()
	detail := &containerInspect{RestartCount: 1}
	ins.checkRestartDetected(q, detail, "app", "abc123", "nginx:1.25", state)
	event := popEvent(q)

	if state.lastRestartCount != 1 {
		t.Errorf("expected lastRestartCount reset to 1, got %d", state.lastRestartCount)
	}
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok after container recreate, got %s", event.EventStatus)
	}
}

func TestCheckRestartDetected_Disabled(t *testing.T) {
	ins := &Instance{
		Targets:         []string{"*"},
		RestartDetected: RestartDetectedCheck{},
	}
	_ = ins.Init()

	state := &containerRestartState{}
	q := safe.NewQueue[*types.Event]()
	detail := &containerInspect{RestartCount: 100}
	ins.checkRestartDetected(q, detail, "app", "abc123", "nginx:1.25", state)

	if q.Len() != 0 {
		t.Error("expected no events when restart_detected is disabled")
	}
}

func TestCheckHealthStatus(t *testing.T) {
	ins := &Instance{Targets: []string{"*"}}
	_ = ins.Init()

	// healthy
	q := safe.NewQueue[*types.Event]()
	detail := &containerInspect{State: containerState{Health: &containerHealth{Status: "healthy"}}}
	ins.checkHealthStatus(q, detail, "app", "abc123", "nginx:1.25")
	event := popEvent(q)
	if event == nil || event.EventStatus != types.EventStatusOk {
		t.Error("expected Ok for healthy")
	}

	// unhealthy
	q = safe.NewQueue[*types.Event]()
	detail = &containerInspect{State: containerState{Health: &containerHealth{
		Status:        "unhealthy",
		FailingStreak: 5,
		Log:           []containerHealthLog{{Output: "connection refused\n"}},
	}}}
	ins.checkHealthStatus(q, detail, "app", "abc123", "nginx:1.25")
	event = popEvent(q)
	if event == nil || event.EventStatus != types.EventStatusCritical {
		t.Error("expected Critical for unhealthy")
	}

	// no healthcheck
	q = safe.NewQueue[*types.Event]()
	detail = &containerInspect{State: containerState{Health: nil}}
	ins.checkHealthStatus(q, detail, "app", "abc123", "nginx:1.25")
	if q.Len() != 0 {
		t.Error("expected no events when no HEALTHCHECK defined")
	}
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{0, "0s"},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{10 * time.Minute, "10m"},
		{65 * time.Minute, "1h 5m"},
		{25 * time.Hour, "1d 1h"},
		{48*time.Hour + 30*time.Minute, "2d 30m"},
	}
	for _, tt := range tests {
		got := humanDuration(tt.d)
		if got != tt.expected {
			t.Errorf("humanDuration(%v) = %q, want %q", tt.d, got, tt.expected)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		b        uint64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1 KiB"},
		{1048576, "1 MiB"},
		{1073741824, "1.0 GiB"},
		{1610612736, "1.5 GiB"},
	}
	for _, tt := range tests {
		got := humanBytes(tt.b)
		if got != tt.expected {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.b, got, tt.expected)
		}
	}
}

func TestParseDockerTime(t *testing.T) {
	// Valid time
	ts := parseDockerTime("2026-02-28T10:30:00.123456789Z")
	if ts.IsZero() {
		t.Error("expected non-zero time")
	}

	// Zero time
	ts = parseDockerTime("0001-01-01T00:00:00Z")
	if !ts.IsZero() {
		t.Error("expected zero time for Docker zero value")
	}

	// Empty
	ts = parseDockerTime("")
	if !ts.IsZero() {
		t.Error("expected zero time for empty string")
	}
}

func TestGetFileCache(t *testing.T) {
	// cgroup v2
	m := memoryStats{Stats: map[string]interface{}{"inactive_file": float64(1000)}}
	if got := getFileCache(m); got != 1000 {
		t.Errorf("expected 1000, got %d", got)
	}

	// cgroup v1 (new)
	m = memoryStats{Stats: map[string]interface{}{"total_inactive_file": float64(2000)}}
	if got := getFileCache(m); got != 2000 {
		t.Errorf("expected 2000, got %d", got)
	}

	// cgroup v1 (old)
	m = memoryStats{Stats: map[string]interface{}{"cache": float64(3000)}}
	if got := getFileCache(m); got != 3000 {
		t.Errorf("expected 3000, got %d", got)
	}

	// no cache data
	m = memoryStats{Stats: map[string]interface{}{}}
	if got := getFileCache(m); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestGetAllocatedCPUs(t *testing.T) {
	// NanoCPUs
	hc := containerHostConfig{NanoCPUs: 2e9}
	if got := getAllocatedCPUs(hc); got != 2.0 {
		t.Errorf("expected 2.0, got %f", got)
	}

	// CpuQuota / CpuPeriod
	hc = containerHostConfig{CpuQuota: 200000, CpuPeriod: 100000}
	if got := getAllocatedCPUs(hc); got != 2.0 {
		t.Errorf("expected 2.0, got %f", got)
	}

	// unlimited
	hc = containerHostConfig{}
	if got := getAllocatedCPUs(hc); got != 0 {
		t.Errorf("expected 0, got %f", got)
	}
}

func TestIsActiveState(t *testing.T) {
	active := []string{"running", "paused", "restarting"}
	inactive := []string{"exited", "dead", "created", "removing"}

	for _, s := range active {
		if !isActiveState(s) {
			t.Errorf("expected %q to be active", s)
		}
	}
	for _, s := range inactive {
		if isActiveState(s) {
			t.Errorf("expected %q to be inactive", s)
		}
	}
}
