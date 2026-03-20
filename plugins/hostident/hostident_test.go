package hostident

import (
	"os"
	"testing"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/types"
)

func TestInit_BothDisabled(t *testing.T) {
	ins := &Instance{}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init() with both checks disabled should not error, got: %v", err)
	}
	if ins.baseHostname != "" || ins.baseIP != "" {
		t.Errorf("baseline should be empty when checks disabled, got hostname=%q ip=%q",
			ins.baseHostname, ins.baseIP)
	}
}

func TestInit_RecordsBaseline(t *testing.T) {
	ins := &Instance{
		HostnameChanged: CheckConfig{Enabled: true},
		IPChanged:       CheckConfig{Enabled: true},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}

	wantHostname, _ := os.Hostname()
	if ins.baseHostname != wantHostname {
		t.Errorf("baseHostname = %q, want %q", ins.baseHostname, wantHostname)
	}
	if ins.baseIP == "" {
		t.Error("baseIP should not be empty after Init with ip.enabled=true")
	}
	if ins.hostSeverity != types.EventStatusWarning {
		t.Errorf("hostSeverity = %q, want %q", ins.hostSeverity, types.EventStatusWarning)
	}
	if ins.ipSeverity != types.EventStatusWarning {
		t.Errorf("ipSeverity = %q, want %q", ins.ipSeverity, types.EventStatusWarning)
	}
}

func TestInit_CustomSeverity(t *testing.T) {
	ins := &Instance{
		HostnameChanged: CheckConfig{Enabled: true, Severity: "critical"},
		IPChanged:       CheckConfig{Enabled: true, Severity: "info"},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	if ins.hostSeverity != types.EventStatusCritical {
		t.Errorf("hostSeverity = %q, want %q", ins.hostSeverity, types.EventStatusCritical)
	}
	if ins.ipSeverity != types.EventStatusInfo {
		t.Errorf("ipSeverity = %q, want %q", ins.ipSeverity, types.EventStatusInfo)
	}
}

func TestInit_InvalidSeverity(t *testing.T) {
	ins := &Instance{
		HostnameChanged: CheckConfig{Enabled: true, Severity: "panic"},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("Init() should fail with invalid severity")
	}
}

func TestGather_HostnameUnchanged(t *testing.T) {
	hostname, _ := os.Hostname()
	ins := &Instance{
		HostnameChanged: CheckConfig{Enabled: true},
		hostSeverity:    types.EventStatusWarning,
		baseHostname:    hostname,
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	events := q.PopBackAll()
	if events[0].EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok status, got %q", events[0].EventStatus)
	}
}

func TestGather_HostnameChanged(t *testing.T) {
	ins := &Instance{
		HostnameChanged: CheckConfig{Enabled: true},
		hostSeverity:    types.EventStatusWarning,
		baseHostname:    "old-hostname-that-does-not-exist",
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	events := q.PopBackAll()
	if events[0].EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning status, got %q", events[0].EventStatus)
	}
}

func TestGather_HostnameChangedCritical(t *testing.T) {
	ins := &Instance{
		HostnameChanged: CheckConfig{Enabled: true, Severity: "Critical"},
		hostSeverity:    types.EventStatusCritical,
		baseHostname:    "old-hostname-that-does-not-exist",
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	events := q.PopBackAll()
	if events[0].EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical status, got %q", events[0].EventStatus)
	}
}

func TestGather_IPUnchanged(t *testing.T) {
	currentIP := config.DetectIP()
	if currentIP == "" {
		t.Skip("cannot detect IP on this machine")
	}

	ins := &Instance{
		IPChanged:  CheckConfig{Enabled: true},
		ipSeverity: types.EventStatusWarning,
		baseIP:     currentIP,
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	events := q.PopBackAll()
	if events[0].EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok status, got %q", events[0].EventStatus)
	}
}

func TestParseSeverity(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"", types.EventStatusWarning, false},
		{"warning", types.EventStatusWarning, false},
		{"Warning", types.EventStatusWarning, false},
		{"critical", types.EventStatusCritical, false},
		{"CRITICAL", types.EventStatusCritical, false},
		{"info", types.EventStatusInfo, false},
		{"Info", types.EventStatusInfo, false},
		{"panic", "", true},
		{"ok", "", true},
	}
	for _, tt := range tests {
		got, err := parseSeverity(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseSeverity(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("parseSeverity(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
