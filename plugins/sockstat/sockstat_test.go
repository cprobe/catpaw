package sockstat

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
)

func TestInit_PlatformCheck(t *testing.T) {
	ins := &Instance{}
	err := ins.Init()
	if runtime.GOOS != "linux" {
		if err == nil {
			t.Error("expected error on non-linux platform")
		}
		return
	}
	if err != nil {
		t.Errorf("unexpected error on linux: %v", err)
	}
}

func TestInit_Validation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	tests := []struct {
		name    string
		ins     Instance
		wantErr bool
	}{
		{
			name: "valid thresholds",
			ins:  Instance{ListenOverflow: ListenOverflowCheck{WarnGe: 1, CriticalGe: 100}},
		},
		{
			name: "warn only",
			ins:  Instance{ListenOverflow: ListenOverflowCheck{WarnGe: 1}},
		},
		{
			name: "critical only",
			ins:  Instance{ListenOverflow: ListenOverflowCheck{CriticalGe: 100}},
		},
		{
			name: "no thresholds - silent skip",
			ins:  Instance{},
		},
		{
			name: "large thresholds - valid (not percentage)",
			ins:  Instance{ListenOverflow: ListenOverflowCheck{WarnGe: 500, CriticalGe: 10000}},
		},
		{
			name:    "warn_ge >= critical_ge",
			ins:     Instance{ListenOverflow: ListenOverflowCheck{WarnGe: 100, CriticalGe: 50}},
			wantErr: true,
		},
		{
			name:    "warn_ge == critical_ge",
			ins:     Instance{ListenOverflow: ListenOverflowCheck{WarnGe: 100, CriticalGe: 100}},
			wantErr: true,
		},
		{
			name:    "negative warn_ge",
			ins:     Instance{ListenOverflow: ListenOverflowCheck{WarnGe: -1}},
			wantErr: true,
		},
		{
			name:    "negative critical_ge",
			ins:     Instance{ListenOverflow: ListenOverflowCheck{CriticalGe: -5}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.ins.Init()
			if tt.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGather_SkipWhenUnconfigured(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 0 {
		t.Errorf("expected 0 events when unconfigured, got %d", q.Len())
	}
}

const sampleNetstat = `TcpExt: SyncookiesSent SyncookiesRecv SyncookiesFailed EmbryonicRsts PruneCalled RcvPruned OfoPruned OutOfWindowIcmps LockDroppedIcmps ArpFilter TW TWRecycled TWKilled PAWSActive PAWSEstab DelayedACKs DelayedACKLocked DelayedACKLost ListenOverflows ListenDrops TCPHPHits TCPPureAcks TCPHPAcks
TcpExt: 0 0 0 0 0 0 0 0 0 0 100 0 0 0 0 500 0 10 789 790 1000 2000 3000
IpExt: InNoRoutes InTruncatedPkts InMcastPkts OutMcastPkts InBcastPkts OutBcastPkts InOctets OutOctets InMcastOctets OutMcastOctets InBcastOctets OutBcastOctets InCsumErrors InNoECTPkts InECT1Pkts InECT0Pkts InCEPkts
IpExt: 0 0 0 0 0 0 123456 654321 0 0 0 0 0 100000 0 0 0
`

func setupMockNetstat(t *testing.T, content string) func() {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "netstat")
	os.WriteFile(path, []byte(content), 0644)

	origPath := netstatPath
	netstatPath = path
	return func() { netstatPath = origPath }
}

func TestReadListenStats_ValidData(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	cleanup := setupMockNetstat(t, sampleNetstat)
	defer cleanup()

	overflows, drops, err := readListenStats()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overflows != 789 {
		t.Errorf("expected overflows 789, got %d", overflows)
	}
	if drops != 790 {
		t.Errorf("expected drops 790, got %d", drops)
	}
}

func TestReadListenStats_NoTcpExt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	cleanup := setupMockNetstat(t, `IpExt: InNoRoutes
IpExt: 0
`)
	defer cleanup()

	_, _, err := readListenStats()
	if err == nil {
		t.Error("expected error for missing TcpExt")
	}
}

func TestReadListenStats_NoListenOverflows(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	cleanup := setupMockNetstat(t, `TcpExt: SyncookiesSent SyncookiesRecv
TcpExt: 0 0
`)
	defer cleanup()

	_, _, err := readListenStats()
	if err == nil {
		t.Error("expected error for missing ListenOverflows field")
	}
}

func TestReadListenStats_HeaderValueMismatch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	cleanup := setupMockNetstat(t, `TcpExt: SyncookiesSent SyncookiesRecv ListenOverflows
TcpExt: 0 0
`)
	defer cleanup()

	_, _, err := readListenStats()
	if err == nil {
		t.Error("expected error for header/value count mismatch")
	}
}

func TestReadListenStats_ParseError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	cleanup := setupMockNetstat(t, `TcpExt: ListenOverflows ListenDrops
TcpExt: not_a_number 0
`)
	defer cleanup()

	_, _, err := readListenStats()
	if err == nil {
		t.Error("expected parse error")
	}
}

func TestReadListenStats_FileNotFound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	origPath := netstatPath
	defer func() { netstatPath = origPath }()
	netstatPath = "/nonexistent/netstat"

	_, _, err := readListenStats()
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestReadListenStats_NoListenDrops(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	cleanup := setupMockNetstat(t, `TcpExt: ListenOverflows
TcpExt: 789
`)
	defer cleanup()

	overflows, drops, err := readListenStats()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overflows != 789 {
		t.Errorf("expected overflows 789, got %d", overflows)
	}
	if drops != 0 {
		t.Errorf("expected drops 0 when field missing, got %d", drops)
	}
}

func TestGather_Baseline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	cleanup := setupMockNetstat(t, sampleNetstat)
	defer cleanup()

	ins := &Instance{
		ListenOverflow: ListenOverflowCheck{WarnGe: 1, CriticalGe: 100},
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	all := q.PopBackAll()
	event := all[0]
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok for baseline, got %s: %s", event.EventStatus, event.Description)
	}
	if event.Labels[types.AttrPrefix+"delta"] != "0" {
		t.Errorf("expected delta=0 for baseline, got %s", event.Labels[types.AttrPrefix+"delta"])
	}
	if !ins.initialized {
		t.Error("expected initialized=true after baseline")
	}
	if ins.prevOverflows != 789 {
		t.Errorf("expected prevOverflows=789, got %d", ins.prevOverflows)
	}
}

func TestGather_DeltaDetection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "netstat")

	origPath := netstatPath
	defer func() { netstatPath = origPath }()
	netstatPath = path

	writeNetstat := func(overflows, drops int) {
		content := fmt.Sprintf(`TcpExt: ListenOverflows ListenDrops
TcpExt: %d %d
`, overflows, drops)
		os.WriteFile(path, []byte(content), 0644)
	}

	ins := &Instance{
		ListenOverflow: ListenOverflowCheck{WarnGe: 1, CriticalGe: 100},
	}

	// First gather: baseline
	writeNetstat(100, 105)
	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	if q.Len() != 1 {
		t.Fatalf("baseline: expected 1 event, got %d", q.Len())
	}
	q.PopBackAll()

	// Second gather: no change → Ok
	writeNetstat(100, 105)
	ins.Gather(q)
	if q.Len() != 1 {
		t.Fatalf("no-change: expected 1 event, got %d", q.Len())
	}
	all := q.PopBackAll()
	if all[0].EventStatus != types.EventStatusOk {
		t.Errorf("no-change: expected Ok, got %s: %s", all[0].EventStatus, all[0].Description)
	}
	if all[0].Labels[types.AttrPrefix+"delta"] != "0" {
		t.Errorf("no-change: expected delta=0, got %s", all[0].Labels[types.AttrPrefix+"delta"])
	}

	// Third gather: small delta → Warning
	writeNetstat(105, 111)
	ins.Gather(q)
	if q.Len() != 1 {
		t.Fatalf("warning: expected 1 event, got %d", q.Len())
	}
	all = q.PopBackAll()
	if all[0].EventStatus != types.EventStatusWarning {
		t.Errorf("warning: expected Warning, got %s: %s", all[0].EventStatus, all[0].Description)
	}
	if all[0].Labels[types.AttrPrefix+"delta"] != "5" {
		t.Errorf("warning: expected delta=5, got %s", all[0].Labels[types.AttrPrefix+"delta"])
	}

	// Fourth gather: large delta → Critical
	writeNetstat(305, 320)
	ins.Gather(q)
	if q.Len() != 1 {
		t.Fatalf("critical: expected 1 event, got %d", q.Len())
	}
	all = q.PopBackAll()
	if all[0].EventStatus != types.EventStatusCritical {
		t.Errorf("critical: expected Critical, got %s: %s", all[0].EventStatus, all[0].Description)
	}
	if all[0].Labels[types.AttrPrefix+"delta"] != "200" {
		t.Errorf("critical: expected delta=200, got %s", all[0].Labels[types.AttrPrefix+"delta"])
	}
}

func TestGather_CounterWraparound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "netstat")

	origPath := netstatPath
	defer func() { netstatPath = origPath }()
	netstatPath = path

	writeNetstat := func(overflows, drops int) {
		content := fmt.Sprintf(`TcpExt: ListenOverflows ListenDrops
TcpExt: %d %d
`, overflows, drops)
		os.WriteFile(path, []byte(content), 0644)
	}

	ins := &Instance{
		ListenOverflow: ListenOverflowCheck{WarnGe: 1, CriticalGe: 100},
	}

	// Baseline with high value
	writeNetstat(1000, 1005)
	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)
	q.PopBackAll()

	// Counter reset (system reboot while catpaw kept running — rare)
	writeNetstat(5, 6)
	ins.Gather(q)
	if q.Len() != 1 {
		t.Fatalf("wraparound: expected 1 event, got %d", q.Len())
	}
	all := q.PopBackAll()
	if all[0].EventStatus != types.EventStatusOk {
		t.Errorf("wraparound: expected Ok (delta=0), got %s: %s", all[0].EventStatus, all[0].Description)
	}
	if all[0].Labels[types.AttrPrefix+"delta"] != "0" {
		t.Errorf("wraparound: expected delta=0, got %s", all[0].Labels[types.AttrPrefix+"delta"])
	}
}

func TestGather_ReadError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sockstat only supports linux")
	}

	origPath := netstatPath
	defer func() { netstatPath = origPath }()
	netstatPath = "/nonexistent/netstat"

	ins := &Instance{
		ListenOverflow: ListenOverflowCheck{WarnGe: 1, CriticalGe: 100},
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event for read error, got %d", q.Len())
	}

	all := q.PopBackAll()
	event := all[0]
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for read error, got %s: %s", event.EventStatus, event.Description)
	}
}
