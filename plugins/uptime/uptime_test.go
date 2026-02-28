package uptime

import (
	"testing"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
)

func TestInitValidation(t *testing.T) {
	tests := []struct {
		name    string
		ins     Instance
		wantErr bool
	}{
		{
			name: "valid critical only",
			ins: Instance{
				RebootDetected: RebootDetectedCheck{CriticalLt: config.Duration(10 * time.Minute)},
			},
		},
		{
			name: "valid warn only",
			ins: Instance{
				RebootDetected: RebootDetectedCheck{WarnLt: config.Duration(1 * time.Hour)},
			},
		},
		{
			name: "valid both thresholds",
			ins: Instance{
				RebootDetected: RebootDetectedCheck{
					CriticalLt: config.Duration(10 * time.Minute),
					WarnLt:     config.Duration(1 * time.Hour),
				},
			},
		},
		{
			name: "no dimension enabled - silent skip",
			ins:  Instance{},
		},
		{
			name: "warn_lt <= critical_lt",
			ins: Instance{
				RebootDetected: RebootDetectedCheck{
					CriticalLt: config.Duration(1 * time.Hour),
					WarnLt:     config.Duration(10 * time.Minute),
				},
			},
			wantErr: true,
		},
		{
			name: "warn_lt == critical_lt",
			ins: Instance{
				RebootDetected: RebootDetectedCheck{
					CriticalLt: config.Duration(30 * time.Minute),
					WarnLt:     config.Duration(30 * time.Minute),
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.ins.Init()
			if tt.wantErr && err == nil {
				t.Errorf("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGatherLive(t *testing.T) {
	ins := &Instance{
		RebootDetected: RebootDetectedCheck{
			CriticalLt: config.Duration(1 * time.Second),
		},
	}

	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep

	if event.Labels["check"] != "uptime::reboot_detected" {
		t.Errorf("unexpected check label: %s", event.Labels["check"])
	}
	if event.Labels["target"] != "system" {
		t.Errorf("unexpected target label: %s", event.Labels["target"])
	}

	if event.EventStatus != types.EventStatusOk {
		t.Errorf("expected Ok status (uptime should be > 1s), got %s: %s",
			event.EventStatus, event.Description)
	}

	if event.Labels[types.AttrPrefix+"uptime"] == "" {
		t.Error("missing _attr_uptime label")
	}
	if event.Labels[types.AttrPrefix+"uptime_seconds"] == "" {
		t.Error("missing _attr_uptime_seconds label")
	}
	if event.Labels[types.AttrPrefix+"boot_time"] == "" {
		t.Error("missing _attr_boot_time label")
	}
}

func TestGatherSkipWhenUnconfigured(t *testing.T) {
	ins := &Instance{}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 0 {
		t.Errorf("expected 0 events when unconfigured, got %d", q.Len())
	}
}

func TestGatherCritical(t *testing.T) {
	// Set critical threshold absurdly high so current uptime is always below it
	ins := &Instance{
		RebootDetected: RebootDetectedCheck{
			CriticalLt: config.Duration(876000 * time.Hour), // ~100 years
		},
	}

	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical, got %s: %s", event.EventStatus, event.Description)
	}
}

func TestGatherWarning(t *testing.T) {
	// critical threshold = 0 (disabled), warn threshold absurdly high
	ins := &Instance{
		RebootDetected: RebootDetectedCheck{
			WarnLt: config.Duration(876000 * time.Hour),
		},
	}

	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %s: %s", event.EventStatus, event.Description)
	}
}

func TestGatherCriticalOverWarning(t *testing.T) {
	// Both thresholds absurdly high — should be Critical (critical takes precedence)
	ins := &Instance{
		RebootDetected: RebootDetectedCheck{
			CriticalLt: config.Duration(876000 * time.Hour),
			WarnLt:     config.Duration(877000 * time.Hour),
		},
	}

	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected an event")
	}
	event := *ep
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical (should take precedence over Warning), got %s: %s",
			event.EventStatus, event.Description)
	}
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{0, "0s"},
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m 30s"},
		{10 * time.Minute, "10m"},
		{3*time.Minute + 20*time.Second, "3m 20s"},
		{1 * time.Hour, "1h"},
		{2*time.Hour + 30*time.Minute, "2h 30m"},
		{2*time.Hour + 30*time.Minute + 15*time.Second, "2h 30m 15s"},
		{24 * time.Hour, "1d"},
		{15*24*time.Hour + 7*time.Hour + 30*time.Minute, "15d 7h 30m"},
		{30 * 24 * time.Hour, "30d"},
		{365 * 24 * time.Hour, "365d"},
		{-(10 * time.Minute), "10m"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := humanDuration(tt.input)
			if got != tt.expected {
				t.Errorf("humanDuration(%v) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
