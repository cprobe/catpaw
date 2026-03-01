package netif

import (
	"testing"

	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
)

// --- Init tests ---

func mockLinux() func() {
	orig := runtimeGOOS
	runtimeGOOS = "linux"
	return func() { runtimeGOOS = orig }
}

func TestInitRejectsNonLinux(t *testing.T) {
	orig := runtimeGOOS
	runtimeGOOS = "darwin"
	defer func() { runtimeGOOS = orig }()

	ins := &Instance{Errors: DeltaCheck{WarnGe: 1}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject on non-linux")
	}
}

func TestInitRejectsNoChecks(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject when no checks configured")
	}
}

func TestInitAcceptsErrorsOnly(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{Errors: DeltaCheck{WarnGe: 1}}
	if err := ins.Init(); err != nil {
		t.Fatalf("errors only should be accepted: %v", err)
	}
	if !ins.hasErrorCheck {
		t.Fatal("hasErrorCheck should be true")
	}
}

func TestInitAcceptsDropsOnly(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{Drops: DeltaCheck{CriticalGe: 50}}
	if err := ins.Init(); err != nil {
		t.Fatalf("drops only should be accepted: %v", err)
	}
	if !ins.hasDropCheck {
		t.Fatal("hasDropCheck should be true")
	}
}

func TestInitAcceptsLinkOnly(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{LinkUp: []LinkSpec{{Interface: "eth0"}}}
	if err := ins.Init(); err != nil {
		t.Fatalf("link_up only should be accepted: %v", err)
	}
}

func TestInitAcceptsAll(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{
		Errors: DeltaCheck{WarnGe: 1, CriticalGe: 100},
		Drops:  DeltaCheck{WarnGe: 1, CriticalGe: 100},
		LinkUp: []LinkSpec{{Interface: "eth0"}},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("all checks should be accepted: %v", err)
	}
}

func TestInitRejectsNegativeThreshold(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{Errors: DeltaCheck{WarnGe: -1}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject negative threshold")
	}
}

func TestInitRejectsWarnGeEqualCritical(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{Errors: DeltaCheck{WarnGe: 10, CriticalGe: 10}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject warn_ge >= critical_ge")
	}
}

func TestInitRejectsWarnGeGreaterThanCritical(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{Drops: DeltaCheck{WarnGe: 100, CriticalGe: 50}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject warn_ge > critical_ge")
	}
}

func TestInitRejectsEmptyLinkInterface(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{LinkUp: []LinkSpec{{Interface: ""}}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty interface name")
	}
}

func TestInitRejectsDuplicateLinkInterface(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{LinkUp: []LinkSpec{
		{Interface: "eth0"},
		{Interface: "eth0"},
	}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject duplicate interface")
	}
}

func TestInitDefaultsSeverityToCritical(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{LinkUp: []LinkSpec{{Interface: "eth0"}}}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.LinkUp[0].Severity != types.EventStatusCritical {
		t.Fatalf("expected default severity Critical, got %q", ins.LinkUp[0].Severity)
	}
}

func TestInitRejectsInvalidSeverity(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{LinkUp: []LinkSpec{{Interface: "eth0", Severity: "Bad"}}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject invalid severity")
	}
}

func TestInitRejectsInvalidGlob(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{
		Errors:  DeltaCheck{WarnGe: 1},
		Exclude: []string{"[invalid"},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject invalid glob pattern")
	}
}

// --- applyFilter tests ---

func TestApplyFilterExclude(t *testing.T) {
	ifaces := []string{"eth0", "lo", "docker0", "veth123", "bond0"}
	exclude := []string{"lo", "docker*", "veth*"}
	result := applyFilter(ifaces, nil, exclude)
	expected := map[string]bool{"eth0": true, "bond0": true}
	if len(result) != len(expected) {
		t.Fatalf("expected %d interfaces, got %d: %v", len(expected), len(result), result)
	}
	for _, r := range result {
		if !expected[r] {
			t.Fatalf("unexpected interface %q in result", r)
		}
	}
}

func TestApplyFilterInclude(t *testing.T) {
	ifaces := []string{"eth0", "eth1", "bond0", "lo"}
	include := []string{"eth*"}
	result := applyFilter(ifaces, include, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 interfaces, got %d: %v", len(result), result)
	}
}

func TestApplyFilterIncludeAndExclude(t *testing.T) {
	ifaces := []string{"eth0", "eth1", "eth2", "bond0"}
	include := []string{"eth*"}
	exclude := []string{"eth1"}
	result := applyFilter(ifaces, include, exclude)
	expected := map[string]bool{"eth0": true, "eth2": true}
	if len(result) != len(expected) {
		t.Fatalf("expected %d interfaces, got %d: %v", len(expected), len(result), result)
	}
	for _, r := range result {
		if !expected[r] {
			t.Fatalf("unexpected interface %q in result", r)
		}
	}
}

func TestApplyFilterEmptyIncludeMatchesAll(t *testing.T) {
	ifaces := []string{"eth0", "bond0"}
	result := applyFilter(ifaces, nil, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
}

// --- matchAny tests ---

func TestMatchAny(t *testing.T) {
	cases := []struct {
		name     string
		iface    string
		patterns []string
		want     bool
	}{
		{"exact", "lo", []string{"lo"}, true},
		{"glob", "docker0", []string{"docker*"}, true},
		{"no match", "eth0", []string{"docker*", "veth*"}, false},
		{"empty patterns", "eth0", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchAny(tc.iface, tc.patterns); got != tc.want {
				t.Fatalf("matchAny(%q, %v) = %v, want %v", tc.iface, tc.patterns, got, tc.want)
			}
		})
	}
}

// --- safeDelta tests ---

func TestSafeDelta(t *testing.T) {
	if d := safeDelta(100, 90); d != 10 {
		t.Fatalf("expected 10, got %d", d)
	}
	if d := safeDelta(50, 50); d != 0 {
		t.Fatalf("expected 0 for equal, got %d", d)
	}
	if d := safeDelta(10, 100); d != 0 {
		t.Fatalf("expected 0 for overflow, got %d", d)
	}
}

// --- Gather tests with mocks ---

func setupMockIO(ifaces []string, counters map[string]*ifCounters, operstates map[string]string) func() {
	origGOOS := runtimeGOOS
	origList := listInterfaces
	origRead := readCounters
	origOper := readOperstate

	runtimeGOOS = "linux"

	listInterfaces = func() ([]string, error) {
		return ifaces, nil
	}
	readCounters = func(iface string) (*ifCounters, error) {
		c, ok := counters[iface]
		if !ok {
			return nil, nil
		}
		return c, nil
	}
	readOperstate = func(iface string) (string, error) {
		s, ok := operstates[iface]
		if !ok {
			return "not_found", nil
		}
		return s, nil
	}

	return func() {
		runtimeGOOS = origGOOS
		listInterfaces = origList
		readCounters = origRead
		readOperstate = origOper
	}
}

func TestGatherBaselineSilent(t *testing.T) {
	cleanup := setupMockIO(
		[]string{"eth0"},
		map[string]*ifCounters{"eth0": {rxErrors: 5, txErrors: 3}},
		nil,
	)
	defer cleanup()

	ins := &Instance{Errors: DeltaCheck{WarnGe: 1}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 0 {
		t.Fatalf("first gather should produce 0 events (silent baseline), got %d", q.Len())
	}
	if !ins.initialized {
		t.Fatal("should be initialized after first gather")
	}
}

func TestGatherErrorsDelta(t *testing.T) {
	counters := map[string]*ifCounters{
		"eth0": {rxErrors: 10, txErrors: 5},
	}
	cleanup := setupMockIO([]string{"eth0"}, counters, nil)
	defer cleanup()

	ins := &Instance{Errors: DeltaCheck{WarnGe: 1, CriticalGe: 100}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q) // baseline
	q = safe.NewQueue[*types.Event]()

	counters["eth0"] = &ifCounters{rxErrors: 22, txErrors: 8}
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	event := *q.PopBack()
	if event.Labels["check"] != "netif::errors" {
		t.Fatalf("expected check netif::errors, got %s", event.Labels["check"])
	}
	if event.Labels["target"] != "eth0" {
		t.Fatalf("expected target eth0, got %s", event.Labels["target"])
	}
	if event.EventStatus != types.EventStatusWarning {
		t.Fatalf("expected Warning (delta 15 >= warn 1), got %s", event.EventStatus)
	}
	if event.Labels[types.AttrPrefix+"delta"] != "15" {
		t.Fatalf("expected delta 15, got %s", event.Labels[types.AttrPrefix+"delta"])
	}
	if event.Labels[types.AttrPrefix+"rx"] != "12" {
		t.Fatalf("expected rx 12, got %s", event.Labels[types.AttrPrefix+"rx"])
	}
	if event.Labels[types.AttrPrefix+"tx"] != "3" {
		t.Fatalf("expected tx 3, got %s", event.Labels[types.AttrPrefix+"tx"])
	}
}

func TestGatherDropsOk(t *testing.T) {
	counters := map[string]*ifCounters{
		"eth0": {rxDropped: 100, txDropped: 50},
	}
	cleanup := setupMockIO([]string{"eth0"}, counters, nil)
	defer cleanup()

	ins := &Instance{Drops: DeltaCheck{WarnGe: 1}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q) // baseline
	q = safe.NewQueue[*types.Event]()

	// no change
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	event := *q.PopBack()
	if event.EventStatus != types.EventStatusOk {
		t.Fatalf("expected Ok (delta 0), got %s", event.EventStatus)
	}
}

func TestGatherErrorsCritical(t *testing.T) {
	counters := map[string]*ifCounters{
		"eth0": {rxErrors: 0},
	}
	cleanup := setupMockIO([]string{"eth0"}, counters, nil)
	defer cleanup()

	ins := &Instance{Errors: DeltaCheck{WarnGe: 1, CriticalGe: 50}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q) // baseline
	q = safe.NewQueue[*types.Event]()

	counters["eth0"] = &ifCounters{rxErrors: 60}
	ins.Gather(q)

	event := *q.PopBack()
	if event.EventStatus != types.EventStatusCritical {
		t.Fatalf("expected Critical (delta 60 >= 50), got %s", event.EventStatus)
	}
}

func TestGatherLinkUp(t *testing.T) {
	cleanup := setupMockIO(nil, nil, map[string]string{"eth0": "up"})
	defer cleanup()

	ins := &Instance{LinkUp: []LinkSpec{{Interface: "eth0"}}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	event := *q.PopBack()
	if event.Labels["check"] != "netif::link" {
		t.Fatalf("expected check netif::link, got %s", event.Labels["check"])
	}
	if event.EventStatus != types.EventStatusOk {
		t.Fatalf("expected Ok, got %s", event.EventStatus)
	}
}

func TestGatherLinkDown(t *testing.T) {
	cleanup := setupMockIO(nil, nil, map[string]string{"eth0": "down"})
	defer cleanup()

	ins := &Instance{LinkUp: []LinkSpec{{Interface: "eth0", Severity: "Critical"}}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	event := *q.PopBack()
	if event.EventStatus != types.EventStatusCritical {
		t.Fatalf("expected Critical, got %s", event.EventStatus)
	}
	if event.Labels[types.AttrPrefix+"operstate"] != "down" {
		t.Fatalf("expected operstate down, got %s", event.Labels[types.AttrPrefix+"operstate"])
	}
}

func TestGatherLinkNotFound(t *testing.T) {
	cleanup := setupMockIO(nil, nil, map[string]string{})
	defer cleanup()

	ins := &Instance{LinkUp: []LinkSpec{{Interface: "eth99"}}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	event := *q.PopBack()
	if event.EventStatus != types.EventStatusCritical {
		t.Fatalf("expected Critical for not found, got %s", event.EventStatus)
	}
	if event.Labels[types.AttrPrefix+"operstate"] != "not_found" {
		t.Fatalf("expected operstate not_found, got %s", event.Labels[types.AttrPrefix+"operstate"])
	}
}

func TestGatherLinkUnknownIsOk(t *testing.T) {
	cleanup := setupMockIO(nil, nil, map[string]string{"tun0": "unknown"})
	defer cleanup()

	ins := &Instance{LinkUp: []LinkSpec{{Interface: "tun0"}}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	event := *q.PopBack()
	if event.EventStatus != types.EventStatusOk {
		t.Fatalf("unknown operstate should be Ok, got %s", event.EventStatus)
	}
}

func TestGatherLinkDormant(t *testing.T) {
	cleanup := setupMockIO(nil, nil, map[string]string{"eth0": "dormant"})
	defer cleanup()

	ins := &Instance{LinkUp: []LinkSpec{{Interface: "eth0"}}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	event := *q.PopBack()
	if event.EventStatus != types.EventStatusCritical {
		t.Fatalf("dormant should trigger alert, got %s", event.EventStatus)
	}
}

func TestGatherNewInterfaceSkipped(t *testing.T) {
	counters := map[string]*ifCounters{
		"eth0": {rxErrors: 10},
	}
	cleanup := setupMockIO([]string{"eth0"}, counters, nil)
	defer cleanup()

	ins := &Instance{Errors: DeltaCheck{WarnGe: 1}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q) // baseline for eth0

	// eth1 appears
	listInterfaces = func() ([]string, error) {
		return []string{"eth0", "eth1"}, nil
	}
	counters["eth1"] = &ifCounters{rxErrors: 999}
	q = safe.NewQueue[*types.Event]()
	ins.Gather(q)

	// only eth0 should produce event (eth1 has no baseline)
	if q.Len() != 1 {
		t.Fatalf("expected 1 event (eth0 only), got %d", q.Len())
	}
	event := *q.PopBack()
	if event.Labels["target"] != "eth0" {
		t.Fatalf("expected target eth0, got %s", event.Labels["target"])
	}
}

func TestGatherLinkOnFirstGather(t *testing.T) {
	counters := map[string]*ifCounters{
		"eth0": {rxErrors: 10},
	}
	cleanup := setupMockIO([]string{"eth0"}, counters, map[string]string{"eth0": "up"})
	defer cleanup()

	ins := &Instance{
		Errors: DeltaCheck{WarnGe: 1},
		LinkUp: []LinkSpec{{Interface: "eth0"}},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	// errors should be silent (baseline), but link should emit
	if q.Len() != 1 {
		t.Fatalf("expected 1 event (link only on first gather), got %d", q.Len())
	}
	event := *q.PopBack()
	if event.Labels["check"] != "netif::link" {
		t.Fatalf("expected netif::link event, got %s", event.Labels["check"])
	}
}

func TestGatherCounterOverflow(t *testing.T) {
	counters := map[string]*ifCounters{
		"eth0": {rxErrors: 100},
	}
	cleanup := setupMockIO([]string{"eth0"}, counters, nil)
	defer cleanup()

	ins := &Instance{Errors: DeltaCheck{WarnGe: 1}}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q) // baseline
	q = safe.NewQueue[*types.Event]()

	// simulate counter wrap (current < prev)
	counters["eth0"] = &ifCounters{rxErrors: 50}
	ins.Gather(q)

	event := *q.PopBack()
	if event.EventStatus != types.EventStatusOk {
		t.Fatalf("counter overflow should be treated as delta 0 (Ok), got %s", event.EventStatus)
	}
}
