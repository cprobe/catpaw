package tcpstate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
)

// --- helpers ---

func mockLinux() func() {
	orig := runtimeGOOS
	runtimeGOOS = "linux"
	return func() { runtimeGOOS = orig }
}

func mockQueryStates(counts *stateCounts, err error) func() {
	orig := queryStatesFn
	queryStatesFn = func() (*stateCounts, error) { return counts, err }
	return func() { queryStatesFn = orig }
}

func mockReadTimeWait(value uint64, err error) func() {
	orig := readTimeWaitFn
	readTimeWaitFn = func() (uint64, error) { return value, err }
	return func() { readTimeWaitFn = orig }
}

func popEvent(t *testing.T, q *safe.Queue[*types.Event]) *types.Event {
	t.Helper()
	ptr := q.PopBack()
	if ptr == nil {
		t.Fatal("expected an event but queue is empty")
	}
	return *ptr
}

// --- Init tests ---

func TestInitRejectsNonLinux(t *testing.T) {
	orig := runtimeGOOS
	runtimeGOOS = "darwin"
	defer func() { runtimeGOOS = orig }()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100}}
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

func TestInitRejectsNegativeThreshold(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{CloseWait: StateCheck{WarnGe: -1}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject negative threshold")
	}
}

func TestInitRejectsWarnGeEqualCritical(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{CloseWait: StateCheck{WarnGe: 100, CriticalGe: 100}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject warn_ge == critical_ge")
	}
}

func TestInitRejectsWarnGeAboveCritical(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{TimeWait: StateCheck{WarnGe: 5000, CriticalGe: 1000}}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject warn_ge > critical_ge")
	}
}

func TestInitAcceptsCloseWaitOnly(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{CloseWait: StateCheck{WarnGe: 100, CriticalGe: 1000}}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ins.hasCloseWaitCheck {
		t.Fatal("hasCloseWaitCheck should be true")
	}
	if ins.hasTimeWaitCheck {
		t.Fatal("hasTimeWaitCheck should be false")
	}
}

func TestInitAcceptsTimeWaitOnly(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{TimeWait: StateCheck{WarnGe: 5000}}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.hasCloseWaitCheck {
		t.Fatal("hasCloseWaitCheck should be false")
	}
	if !ins.hasTimeWaitCheck {
		t.Fatal("hasTimeWaitCheck should be true")
	}
}

func TestInitAcceptsBoth(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{
		CloseWait: StateCheck{WarnGe: 100, CriticalGe: 1000},
		TimeWait:  StateCheck{WarnGe: 5000, CriticalGe: 20000},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ins.hasCloseWaitCheck || !ins.hasTimeWaitCheck {
		t.Fatal("both checks should be enabled")
	}
}

func TestInitAcceptsWarnOnlyWithoutCritical(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{CloseWait: StateCheck{WarnGe: 50}}
	if err := ins.Init(); err != nil {
		t.Fatalf("warn-only should be accepted: %v", err)
	}
}

func TestInitAcceptsCriticalOnlyWithoutWarn(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{TimeWait: StateCheck{CriticalGe: 20000}}
	if err := ins.Init(); err != nil {
		t.Fatalf("critical-only should be accepted: %v", err)
	}
}

// --- Gather: Netlink path (close_wait configured) ---

func TestGatherNetlinkOk(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{established: 5000, closeWait: 10, timeWait: 200}, nil)()

	ins := &Instance{
		CloseWait: StateCheck{WarnGe: 100, CriticalGe: 1000},
		TimeWait:  StateCheck{WarnGe: 5000, CriticalGe: 20000},
	}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 2 {
		t.Fatalf("expected 2 events, got %d", q.Len())
	}

	// Events are pushed front, so first pushed = last out; PopBack retrieves FIFO order
	cwEvent := popEvent(t, q)
	if cwEvent.Labels["check"] != "tcpstate::close_wait" {
		t.Fatalf("expected close_wait check, got %s", cwEvent.Labels["check"])
	}
	if cwEvent.EventStatus != types.EventStatusOk {
		t.Fatalf("close_wait should be Ok, got %q", cwEvent.EventStatus)
	}
	if cwEvent.Labels[types.AttrPrefix+"count"] != "10" {
		t.Fatalf("expected count=10, got %s", cwEvent.Labels[types.AttrPrefix+"count"])
	}
	if cwEvent.Labels[types.AttrPrefix+"established"] != "5000" {
		t.Fatalf("expected established=5000, got %s", cwEvent.Labels[types.AttrPrefix+"established"])
	}

	twEvent := popEvent(t, q)
	if twEvent.Labels["check"] != "tcpstate::time_wait" {
		t.Fatalf("expected time_wait check, got %s", twEvent.Labels["check"])
	}
}

func TestGatherNetlinkWarning(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{established: 10000, closeWait: 350, timeWait: 100}, nil)()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100, CriticalGe: 1000}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	e := popEvent(t, q)
	if e.EventStatus != types.EventStatusWarning {
		t.Fatalf("expected Warning, got %q", e.EventStatus)
	}
	if !strings.Contains(e.Description, "350 CLOSE_WAIT") {
		t.Fatalf("description should mention count: %s", e.Description)
	}
}

func TestGatherNetlinkCritical(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{established: 10000, closeWait: 2000, timeWait: 100}, nil)()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100, CriticalGe: 1000}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	e := popEvent(t, q)
	if e.EventStatus != types.EventStatusCritical {
		t.Fatalf("expected Critical, got %q", e.EventStatus)
	}
}

func TestGatherNetlinkError(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(nil, fmt.Errorf("netlink socket: permission denied"))()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 error event, got %d", q.Len())
	}
	e := popEvent(t, q)
	if e.EventStatus != types.EventStatusCritical {
		t.Fatalf("error event should be Critical, got %q", e.EventStatus)
	}
	if !strings.Contains(e.Description, "netlink") {
		t.Fatalf("description should mention netlink: %s", e.Description)
	}
}

func TestGatherNetlinkCloseWaitOnlyNoTimeWaitEvent(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{established: 1000, closeWait: 5, timeWait: 8000}, nil)()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event (close_wait only), got %d", q.Len())
	}
	e := popEvent(t, q)
	if e.Labels["check"] != "tcpstate::close_wait" {
		t.Fatalf("expected close_wait, got %s", e.Labels["check"])
	}
}

func TestGatherNetlinkBothChecks(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{established: 10000, closeWait: 500, timeWait: 8000}, nil)()

	ins := &Instance{
		CloseWait: StateCheck{WarnGe: 100, CriticalGe: 1000},
		TimeWait:  StateCheck{WarnGe: 5000, CriticalGe: 20000},
	}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 2 {
		t.Fatalf("expected 2 events, got %d", q.Len())
	}

	e1 := popEvent(t, q)
	e2 := popEvent(t, q)

	if e1.Labels["check"] != "tcpstate::close_wait" || e2.Labels["check"] != "tcpstate::time_wait" {
		t.Fatalf("expected close_wait then time_wait, got %s and %s",
			e1.Labels["check"], e2.Labels["check"])
	}
	if e1.EventStatus != types.EventStatusWarning {
		t.Fatalf("close_wait 500 >= 100 should be Warning, got %q", e1.EventStatus)
	}
	if e2.EventStatus != types.EventStatusWarning {
		t.Fatalf("time_wait 8000 >= 5000 should be Warning, got %q", e2.EventStatus)
	}
}

// --- Gather: sockstat path (time_wait only) ---

func TestGatherSockstatOk(t *testing.T) {
	defer mockLinux()()
	defer mockReadTimeWait(1200, nil)()

	ins := &Instance{TimeWait: StateCheck{WarnGe: 5000, CriticalGe: 20000}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	e := popEvent(t, q)
	if e.Labels["check"] != "tcpstate::time_wait" {
		t.Fatalf("expected time_wait, got %s", e.Labels["check"])
	}
	if e.EventStatus != types.EventStatusOk {
		t.Fatalf("expected Ok, got %q", e.EventStatus)
	}
	if _, ok := e.Labels[types.AttrPrefix+"established"]; ok {
		t.Fatal("sockstat path should NOT have _attr_established")
	}
}

func TestGatherSockstatWarning(t *testing.T) {
	defer mockLinux()()
	defer mockReadTimeWait(8000, nil)()

	ins := &Instance{TimeWait: StateCheck{WarnGe: 5000, CriticalGe: 20000}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	e := popEvent(t, q)
	if e.EventStatus != types.EventStatusWarning {
		t.Fatalf("expected Warning, got %q", e.EventStatus)
	}
}

func TestGatherSockstatCritical(t *testing.T) {
	defer mockLinux()()
	defer mockReadTimeWait(25000, nil)()

	ins := &Instance{TimeWait: StateCheck{WarnGe: 5000, CriticalGe: 20000}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	e := popEvent(t, q)
	if e.EventStatus != types.EventStatusCritical {
		t.Fatalf("expected Critical, got %q", e.EventStatus)
	}
}

func TestGatherSockstatError(t *testing.T) {
	defer mockLinux()()
	defer mockReadTimeWait(0, fmt.Errorf("read /proc/net/sockstat: permission denied"))()

	ins := &Instance{TimeWait: StateCheck{WarnGe: 5000}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	e := popEvent(t, q)
	if e.EventStatus != types.EventStatusCritical {
		t.Fatalf("error should be Critical, got %q", e.EventStatus)
	}
	if !strings.Contains(e.Description, "sockstat") {
		t.Fatalf("description should mention sockstat: %s", e.Description)
	}
}

// --- Gather: exact threshold boundary ---

func TestGatherExactWarnThreshold(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{closeWait: 100}, nil)()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100, CriticalGe: 1000}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	e := popEvent(t, q)
	if e.EventStatus != types.EventStatusWarning {
		t.Fatalf("count == warn_ge should trigger Warning, got %q", e.EventStatus)
	}
}

func TestGatherExactCriticalThreshold(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{closeWait: 1000}, nil)()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100, CriticalGe: 1000}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	e := popEvent(t, q)
	if e.EventStatus != types.EventStatusCritical {
		t.Fatalf("count == critical_ge should trigger Critical, got %q", e.EventStatus)
	}
}

func TestGatherZeroCountOk(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{established: 5000, closeWait: 0, timeWait: 0}, nil)()

	ins := &Instance{
		CloseWait: StateCheck{WarnGe: 100},
		TimeWait:  StateCheck{WarnGe: 5000},
	}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 2 {
		t.Fatalf("expected 2 events, got %d", q.Len())
	}
	for i := 0; i < 2; i++ {
		e := popEvent(t, q)
		if e.EventStatus != types.EventStatusOk {
			t.Fatalf("zero count should be Ok, got %q for %s", e.EventStatus, e.Labels["check"])
		}
	}
}

// --- title_rule ---

func TestDefaultTitleRule(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{closeWait: 10}, nil)()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	e := popEvent(t, q)
	if e.TitleRule != "[check]" {
		t.Fatalf("default title_rule should be [check], got %q", e.TitleRule)
	}
}

func TestCustomTitleRule(t *testing.T) {
	defer mockLinux()()
	defer mockQueryStates(&stateCounts{closeWait: 10}, nil)()

	ins := &Instance{CloseWait: StateCheck{WarnGe: 100, TitleRule: "[check] [target]"}}
	ins.Init()

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	e := popEvent(t, q)
	if e.TitleRule != "[check] [target]" {
		t.Fatalf("expected custom title_rule, got %q", e.TitleRule)
	}
}

// --- error event check label ---

func TestErrorEventCheckLabelCloseWait(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{CloseWait: StateCheck{WarnGe: 100}, TimeWait: StateCheck{WarnGe: 5000}}
	ins.Init()

	e := ins.buildErrorEvent("test error")
	if e.Labels["check"] != "tcpstate::close_wait" {
		t.Fatalf("error event should use close_wait when configured, got %s", e.Labels["check"])
	}
}

func TestErrorEventCheckLabelTimeWait(t *testing.T) {
	defer mockLinux()()
	ins := &Instance{TimeWait: StateCheck{WarnGe: 5000}}
	ins.Init()

	e := ins.buildErrorEvent("test error")
	if e.Labels["check"] != "tcpstate::time_wait" {
		t.Fatalf("error event should use time_wait when close_wait not configured, got %s", e.Labels["check"])
	}
}

// --- parseSockstatTimeWait ---

func TestParseSockstatNormal(t *testing.T) {
	data := []byte(`sockets: used 1234
TCP: inuse 500 orphan 10 tw 3000 alloc 600 mem 50
UDP: inuse 20 mem 5
UDPLITE: inuse 0
RAW: inuse 0
FRAG: inuse 0 memory 0
`)
	v, err := parseSockstatTimeWait(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 3000 {
		t.Fatalf("expected 3000, got %d", v)
	}
}

func TestParseSockstatZeroTw(t *testing.T) {
	data := []byte("TCP: inuse 10 orphan 0 tw 0 alloc 10 mem 1\n")
	v, err := parseSockstatTimeWait(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 0 {
		t.Fatalf("expected 0, got %d", v)
	}
}

func TestParseSockstatLargeValue(t *testing.T) {
	data := []byte("TCP: inuse 50000 orphan 100 tw 120000 alloc 60000 mem 5000\n")
	v, err := parseSockstatTimeWait(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 120000 {
		t.Fatalf("expected 120000, got %d", v)
	}
}

func TestParseSockstatMissingTwField(t *testing.T) {
	data := []byte("TCP: inuse 500 orphan 10 alloc 600 mem 50\n")
	_, err := parseSockstatTimeWait(data)
	if err == nil {
		t.Fatal("should error when tw field is missing")
	}
}

func TestParseSockstatNoTcpLine(t *testing.T) {
	data := []byte("sockets: used 1234\nUDP: inuse 20 mem 5\n")
	_, err := parseSockstatTimeWait(data)
	if err == nil {
		t.Fatal("should error when TCP line is missing")
	}
}

func TestParseSockstatEmptyData(t *testing.T) {
	_, err := parseSockstatTimeWait([]byte{})
	if err == nil {
		t.Fatal("should error on empty data")
	}
}

func TestParseSockstatInvalidTwValue(t *testing.T) {
	data := []byte("TCP: inuse 500 orphan 10 tw abc alloc 600 mem 50\n")
	_, err := parseSockstatTimeWait(data)
	if err == nil {
		t.Fatal("should error on invalid tw value")
	}
}

func TestParseSockstatTwAtEnd(t *testing.T) {
	// tw is the last key-value pair with no trailing fields
	data := []byte("TCP: inuse 500 orphan 10 tw 999\n")
	v, err := parseSockstatTimeWait(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 999 {
		t.Fatalf("expected 999, got %d", v)
	}
}

// --- validateThresholds ---

func TestValidateThresholdsAllZero(t *testing.T) {
	if err := validateThresholds("test", StateCheck{}); err != nil {
		t.Fatalf("all-zero should pass: %v", err)
	}
}

func TestValidateThresholdsWarnOnly(t *testing.T) {
	if err := validateThresholds("test", StateCheck{WarnGe: 100}); err != nil {
		t.Fatalf("warn-only should pass: %v", err)
	}
}

func TestValidateThresholdsCriticalOnly(t *testing.T) {
	if err := validateThresholds("test", StateCheck{CriticalGe: 1000}); err != nil {
		t.Fatalf("critical-only should pass: %v", err)
	}
}

func TestValidateThresholdsValid(t *testing.T) {
	if err := validateThresholds("test", StateCheck{WarnGe: 100, CriticalGe: 1000}); err != nil {
		t.Fatalf("valid thresholds should pass: %v", err)
	}
}

func TestValidateThresholdsWarnEqCritical(t *testing.T) {
	if err := validateThresholds("test", StateCheck{WarnGe: 100, CriticalGe: 100}); err == nil {
		t.Fatal("warn == critical should fail")
	}
}

func TestValidateThresholdsWarnAboveCritical(t *testing.T) {
	if err := validateThresholds("test", StateCheck{WarnGe: 1000, CriticalGe: 100}); err == nil {
		t.Fatal("warn > critical should fail")
	}
}

func TestValidateThresholdsNegativeWarn(t *testing.T) {
	if err := validateThresholds("test", StateCheck{WarnGe: -1}); err == nil {
		t.Fatal("negative warn should fail")
	}
}

func TestValidateThresholdsNegativeCritical(t *testing.T) {
	if err := validateThresholds("test", StateCheck{CriticalGe: -1}); err == nil {
		t.Fatal("negative critical should fail")
	}
}
