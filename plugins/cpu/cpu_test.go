package cpu

import (
	"testing"

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
			name: "valid cpu_usage only",
			ins: Instance{
				CpuUsage: CpuUsageCheck{WarnGe: 80, CriticalGe: 90},
			},
		},
		{
			name: "valid load_average only",
			ins: Instance{
				LoadAverage: LoadAverageCheck{WarnGe: 2, CriticalGe: 5},
			},
		},
		{
			name: "valid both dimensions",
			ins: Instance{
				CpuUsage:    CpuUsageCheck{WarnGe: 90, CriticalGe: 95},
				LoadAverage: LoadAverageCheck{WarnGe: 3, CriticalGe: 5},
			},
		},
		{
			name: "valid warn only",
			ins: Instance{
				CpuUsage: CpuUsageCheck{WarnGe: 80},
			},
		},
		{
			name: "valid critical only",
			ins: Instance{
				LoadAverage: LoadAverageCheck{CriticalGe: 5},
			},
		},
		{
			name: "no dimension enabled - silent skip",
			ins:  Instance{},
		},
		{
			name: "cpu warn >= critical",
			ins: Instance{
				CpuUsage: CpuUsageCheck{WarnGe: 95, CriticalGe: 90},
			},
			wantErr: true,
		},
		{
			name: "cpu warn == critical",
			ins: Instance{
				CpuUsage: CpuUsageCheck{WarnGe: 90, CriticalGe: 90},
			},
			wantErr: true,
		},
		{
			name: "load warn >= critical",
			ins: Instance{
				LoadAverage: LoadAverageCheck{WarnGe: 5, CriticalGe: 3},
			},
			wantErr: true,
		},
		{
			name: "invalid period",
			ins: Instance{
				LoadAverage: LoadAverageCheck{WarnGe: 3, CriticalGe: 5, Period: "10m"},
			},
			wantErr: true,
		},
		{
			name: "valid period 1m",
			ins: Instance{
				LoadAverage: LoadAverageCheck{WarnGe: 3, CriticalGe: 5, Period: "1m"},
			},
		},
		{
			name: "valid period 15m",
			ins: Instance{
				LoadAverage: LoadAverageCheck{WarnGe: 3, CriticalGe: 5, Period: "15m"},
			},
		},
		{
			name: "empty period defaults to 5m",
			ins: Instance{
				LoadAverage: LoadAverageCheck{WarnGe: 3, CriticalGe: 5, Period: ""},
			},
		},
		{
			name: "cpu_usage warn_ge exceeds 100",
			ins: Instance{
				CpuUsage: CpuUsageCheck{WarnGe: 800, CriticalGe: 900},
			},
			wantErr: true,
		},
		{
			name: "cpu_usage critical_ge exceeds 100",
			ins: Instance{
				CpuUsage: CpuUsageCheck{CriticalGe: 150},
			},
			wantErr: true,
		},
		{
			name: "cpu_usage boundary 100 is valid",
			ins: Instance{
				CpuUsage: CpuUsageCheck{WarnGe: 95, CriticalGe: 100},
			},
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

func TestInitDefaultPeriod(t *testing.T) {
	ins := &Instance{
		LoadAverage: LoadAverageCheck{WarnGe: 3, CriticalGe: 5},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if ins.LoadAverage.Period != "5m" {
		t.Errorf("expected period=5m, got %s", ins.LoadAverage.Period)
	}
}

func TestInitCachesCpuCores(t *testing.T) {
	ins := &Instance{
		CpuUsage: CpuUsageCheck{WarnGe: 80, CriticalGe: 90},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if ins.cpuCores <= 0 {
		t.Errorf("expected cpuCores > 0, got %d", ins.cpuCores)
	}
}

func TestGatherCpuUsage(t *testing.T) {
	ins := &Instance{
		CpuUsage: CpuUsageCheck{WarnGe: 90, CriticalGe: 95},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkCpuUsage(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected event but got nil")
	}
	event := *ep
	if event.Labels["check"] != "cpu::cpu_usage" {
		t.Errorf("expected check=cpu::cpu_usage, got %s", event.Labels["check"])
	}
	if event.Labels["target"] != "cpu" {
		t.Errorf("expected target=cpu, got %s", event.Labels["target"])
	}

	// Init() warms up gopsutil's snapshot, so the normal path should be taken.
	// If the API returned an error (Critical without attr), skip attr checks.
	if event.EventStatus == types.EventStatusCritical && event.Labels[types.AttrPrefix+"cpu_usage"] == "" {
		t.Log("cpu.Percent returned an error, skipping attr checks")
		return
	}
	if event.Labels[types.AttrPrefix+"cpu_usage"] == "" {
		t.Error("expected _attr_cpu_usage to be set")
	}
	if event.Labels[types.AttrPrefix+"cpu_cores"] == "" {
		t.Error("expected _attr_cpu_cores to be set")
	}
}

func TestGatherLoadAverage(t *testing.T) {
	ins := &Instance{
		LoadAverage: LoadAverageCheck{WarnGe: 3, CriticalGe: 5},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkLoadAverage(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected event but got nil")
	}
	event := *ep
	if event.Labels["check"] != "cpu::load_average" {
		t.Errorf("expected check=cpu::load_average, got %s", event.Labels["check"])
	}
	if event.Labels[types.AttrPrefix+"load1"] == "" {
		t.Error("expected _attr_load1 to be set")
	}
	if event.Labels[types.AttrPrefix+"load5"] == "" {
		t.Error("expected _attr_load5 to be set")
	}
	if event.Labels[types.AttrPrefix+"load15"] == "" {
		t.Error("expected _attr_load15 to be set")
	}
	if event.Labels[types.AttrPrefix+"per_core_load"] == "" {
		t.Error("expected _attr_per_core_load to be set")
	}
	if event.Labels[types.AttrPrefix+"period"] != "5m" {
		t.Errorf("expected _attr_period=5m, got %s", event.Labels[types.AttrPrefix+"period"])
	}
}

func TestGatherSkipsDisabledDimension(t *testing.T) {
	ins := &Instance{
		CpuUsage: CpuUsageCheck{WarnGe: 90, CriticalGe: 95},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	for q.Len() > 0 {
		ep := q.PopBack()
		if ep == nil {
			continue
		}
		event := *ep
		if event.Labels["check"] == "cpu::load_average" {
			t.Error("load_average should be skipped when thresholds are 0")
		}
	}
}
