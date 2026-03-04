package diskio

import (
	"os"
	"testing"

	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
	"github.com/shirou/gopsutil/v3/disk"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	logger.Logger = zap.NewNop().Sugar()
	os.Exit(m.Run())
}

func TestInit_DisabledByConfig(t *testing.T) {
	ins := &Instance{
		IOLatency: IOLatencyCheck{Enabled: false},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init() should not error when disabled, got: %v", err)
	}
	if !ins.disabled {
		t.Error("expected disabled=true when IOLatency.Enabled=false")
	}
}

func TestInit_ThresholdValidation(t *testing.T) {
	tests := []struct {
		name    string
		warn    float64
		crit    float64
		wantErr bool
	}{
		{"both zero (auto)", 0, 0, false},
		{"warn < crit", 10, 50, false},
		{"warn only", 10, 0, false},
		{"crit only", 0, 50, false},
		{"warn == crit", 50, 50, true},
		{"warn > crit", 100, 50, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ins := &Instance{
				IOLatency: IOLatencyCheck{
					Enabled:    true,
					WarnGe:     tt.warn,
					CriticalGe: tt.crit,
				},
			}
			err := ins.Init()
			// On non-Linux, Init() disables silently — not an error.
			if ins.disabled {
				t.Skip("skipped: not running on Linux")
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("Init() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestSafeDelta(t *testing.T) {
	tests := []struct {
		v2, v1 uint64
		want   uint64
	}{
		{100, 50, 50},
		{0, 0, 0},
		{50, 100, 0}, // counter wrap → 0
		{1000, 999, 1},
	}
	for _, tt := range tests {
		got := safeDelta(tt.v2, tt.v1)
		if got != tt.want {
			t.Errorf("safeDelta(%d, %d) = %d, want %d", tt.v2, tt.v1, got, tt.want)
		}
	}
}

func TestShouldSkipVirtual(t *testing.T) {
	tests := []struct {
		name string
		skip bool
	}{
		{"loop0", true},
		{"loop12", true},
		{"ram0", true},
		{"sda", false},
		{"nvme0n1", false},
		{"dm-0", false},
		{"md0", false},
	}
	for _, tt := range tests {
		got := shouldSkipVirtual(tt.name)
		if got != tt.skip {
			t.Errorf("shouldSkipVirtual(%q) = %v, want %v", tt.name, got, tt.skip)
		}
	}
}

func TestThresholds_UserOverride(t *testing.T) {
	ins := &Instance{
		IOLatency: IOLatencyCheck{WarnGe: 30, CriticalGe: 150},
	}
	w, c := ins.thresholds("HDD")
	if w != 30 || c != 150 {
		t.Errorf("thresholds(HDD) with override = (%.0f, %.0f), want (30, 150)", w, c)
	}
	w, c = ins.thresholds("NVMe")
	if w != 30 || c != 150 {
		t.Errorf("thresholds(NVMe) with override = (%.0f, %.0f), want (30, 150)", w, c)
	}
}

func TestThresholds_AutoDefaults(t *testing.T) {
	ins := &Instance{}
	tests := []struct {
		devType      string
		wantWarn     float64
		wantCritical float64
	}{
		{"HDD", 50, 200},
		{"SSD", 20, 100},
		{"NVMe", 10, 50},
		{"?", 100, 500},
	}
	for _, tt := range tests {
		w, c := ins.thresholds(tt.devType)
		if w != tt.wantWarn || c != tt.wantCritical {
			t.Errorf("thresholds(%q) = (%.0f, %.0f), want (%.0f, %.0f)",
				tt.devType, w, c, tt.wantWarn, tt.wantCritical)
		}
	}
}

func TestEmitEvent_Idle(t *testing.T) {
	ins := &Instance{
		deviceTypes: map[string]string{"sda": "HDD"},
	}
	q := safe.NewQueue[*types.Event]()
	prev := disk.IOCountersStat{ReadCount: 100, WriteCount: 50}
	cur := disk.IOCountersStat{ReadCount: 100, WriteCount: 50}
	ins.emitEvent(q, "sda", prev, cur, 30)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	ev := q.PopBackAll()[0]
	if ev.EventStatus != types.EventStatusOk {
		t.Errorf("idle device should produce Ok, got %q", ev.EventStatus)
	}
}

func TestEmitEvent_Healthy(t *testing.T) {
	ins := &Instance{
		deviceTypes: map[string]string{"sda": "HDD"},
	}
	q := safe.NewQueue[*types.Event]()
	prev := disk.IOCountersStat{ReadCount: 100, WriteCount: 50, ReadTime: 200, WriteTime: 100, IoTime: 1000}
	cur := disk.IOCountersStat{ReadCount: 200, WriteCount: 100, ReadTime: 210, WriteTime: 105, IoTime: 1100}
	// 150 IOs, 15ms total → await = 0.1ms → healthy for HDD
	ins.emitEvent(q, "sda", prev, cur, 30)

	ev := q.PopBackAll()[0]
	if ev.EventStatus != types.EventStatusOk {
		t.Errorf("low-await device should produce Ok, got %q", ev.EventStatus)
	}
}

func TestEmitEvent_Warning(t *testing.T) {
	ins := &Instance{
		deviceTypes: map[string]string{"sda": "HDD"},
	}
	q := safe.NewQueue[*types.Event]()
	// 100 IOs, 6000ms total → await = 60ms → >= 50ms HDD warn
	prev := disk.IOCountersStat{}
	cur := disk.IOCountersStat{ReadCount: 50, WriteCount: 50, ReadTime: 3000, WriteTime: 3000, IoTime: 28000}
	ins.emitEvent(q, "sda", prev, cur, 30)

	ev := q.PopBackAll()[0]
	if ev.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning, got %q", ev.EventStatus)
	}
}

func TestEmitEvent_Critical(t *testing.T) {
	ins := &Instance{
		deviceTypes: map[string]string{"sda": "HDD"},
	}
	q := safe.NewQueue[*types.Event]()
	// 100 IOs, 30000ms total → await = 300ms → >= 200ms HDD critical
	prev := disk.IOCountersStat{}
	cur := disk.IOCountersStat{ReadCount: 50, WriteCount: 50, ReadTime: 15000, WriteTime: 15000, IoTime: 29000}
	ins.emitEvent(q, "sda", prev, cur, 30)

	ev := q.PopBackAll()[0]
	if ev.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical, got %q", ev.EventStatus)
	}
}

func TestEmitEvent_AttrsPopulated(t *testing.T) {
	ins := &Instance{
		deviceTypes: map[string]string{"nvme0n1": "NVMe"},
	}
	q := safe.NewQueue[*types.Event]()
	prev := disk.IOCountersStat{}
	cur := disk.IOCountersStat{ReadCount: 1000, WriteCount: 500, ReadTime: 100, WriteTime: 50, IoTime: 5000}
	ins.emitEvent(q, "nvme0n1", prev, cur, 30)

	ev := q.PopBackAll()[0]

	requiredAttrs := []string{"device_type", "await_ms", "util_percent", "read_iops", "write_iops", types.AttrThresholdDesc}
	for _, key := range requiredAttrs {
		if _, ok := ev.Attrs[key]; !ok {
			t.Errorf("missing attr %q", key)
		}
	}
	if ev.Attrs["device_type"] != "NVMe" {
		t.Errorf("device_type = %q, want NVMe", ev.Attrs["device_type"])
	}
	if ev.Attrs[types.AttrCurrentValue] == "" {
		t.Error("current_value attr should be set")
	}
}

func TestEmitEvent_CounterWrap(t *testing.T) {
	ins := &Instance{
		deviceTypes: map[string]string{"sda": "SSD"},
	}
	q := safe.NewQueue[*types.Event]()
	// Counter wrapped: cur < prev → safeDelta returns 0 → idle
	prev := disk.IOCountersStat{ReadCount: 1000, WriteCount: 500}
	cur := disk.IOCountersStat{ReadCount: 10, WriteCount: 5}
	ins.emitEvent(q, "sda", prev, cur, 30)

	ev := q.PopBackAll()[0]
	if ev.EventStatus != types.EventStatusOk {
		t.Errorf("counter wrap should produce Ok (idle), got %q", ev.EventStatus)
	}
}

func TestEmitEvent_UserOverrideThreshold(t *testing.T) {
	ins := &Instance{
		IOLatency:   IOLatencyCheck{WarnGe: 5, CriticalGe: 20},
		deviceTypes: map[string]string{"sda": "HDD"},
	}
	q := safe.NewQueue[*types.Event]()
	// 100 IOs, 800ms → await = 8ms → >= 5ms user warn
	prev := disk.IOCountersStat{}
	cur := disk.IOCountersStat{ReadCount: 50, WriteCount: 50, ReadTime: 400, WriteTime: 400, IoTime: 5000}
	ins.emitEvent(q, "sda", prev, cur, 30)

	ev := q.PopBackAll()[0]
	if ev.EventStatus != types.EventStatusWarning {
		t.Errorf("expected Warning with user override, got %q", ev.EventStatus)
	}
}
