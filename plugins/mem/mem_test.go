package mem

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
			name: "valid memory_usage only",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{WarnGe: 80, CriticalGe: 90},
			},
		},
		{
			name: "valid swap_usage only",
			ins: Instance{
				SwapUsage: SwapUsageCheck{WarnGe: 70, CriticalGe: 95},
			},
		},
		{
			name: "valid both dimensions",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{WarnGe: 85, CriticalGe: 90},
				SwapUsage:   SwapUsageCheck{WarnGe: 80, CriticalGe: 95},
			},
		},
		{
			name: "valid warn only",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{WarnGe: 80},
			},
		},
		{
			name: "valid critical only",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{CriticalGe: 90},
			},
		},
		{
			name: "no dimension enabled - silent skip",
			ins:  Instance{},
		},
		{
			name: "memory warn >= critical",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{WarnGe: 90, CriticalGe: 80},
			},
			wantErr: true,
		},
		{
			name: "memory warn == critical",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{WarnGe: 90, CriticalGe: 90},
			},
			wantErr: true,
		},
		{
			name: "swap warn >= critical",
			ins: Instance{
				SwapUsage: SwapUsageCheck{WarnGe: 95, CriticalGe: 80},
			},
			wantErr: true,
		},
		{
			name: "memory_usage warn_ge exceeds 100",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{WarnGe: 850, CriticalGe: 900},
			},
			wantErr: true,
		},
		{
			name: "memory_usage critical_ge exceeds 100",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{CriticalGe: 110},
			},
			wantErr: true,
		},
		{
			name: "swap_usage warn_ge exceeds 100",
			ins: Instance{
				SwapUsage: SwapUsageCheck{WarnGe: 200},
			},
			wantErr: true,
		},
		{
			name: "memory_usage boundary 100 is valid",
			ins: Instance{
				MemoryUsage: MemoryUsageCheck{WarnGe: 95, CriticalGe: 100},
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

func TestGatherMemoryUsage(t *testing.T) {
	ins := &Instance{
		MemoryUsage: MemoryUsageCheck{WarnGe: 85, CriticalGe: 90},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkMemoryUsage(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	ep := q.PopBack()
	if ep == nil {
		t.Fatal("expected event but got nil")
	}
	event := *ep
	if event.Labels["check"] != "mem::memory_usage" {
		t.Errorf("expected check=mem::memory_usage, got %s", event.Labels["check"])
	}
	if event.Labels["target"] != "memory" {
		t.Errorf("expected target=memory, got %s", event.Labels["target"])
	}
	if event.Labels[types.AttrPrefix+"total"] == "" {
		t.Error("expected _attr_total to be set")
	}
	if event.Labels[types.AttrPrefix+"used_percent"] == "" {
		t.Error("expected _attr_used_percent to be set")
	}
}

func TestGatherSwapUsage(t *testing.T) {
	ins := &Instance{
		SwapUsage: SwapUsageCheck{WarnGe: 80, CriticalGe: 95},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	q := safe.NewQueue[*types.Event]()
	ins.checkSwapUsage(q)

	// Swap 可能未启用（Total=0），此时不产出事件
	if q.Len() > 1 {
		t.Fatalf("expected 0 or 1 event, got %d", q.Len())
	}

	if q.Len() == 1 {
		ep := q.PopBack()
		if ep == nil {
			t.Fatal("expected event but got nil")
		}
		event := *ep
		if event.Labels["check"] != "mem::swap_usage" {
			t.Errorf("expected check=mem::swap_usage, got %s", event.Labels["check"])
		}
	}
}

func TestGatherSkipsDisabledDimension(t *testing.T) {
	ins := &Instance{
		MemoryUsage: MemoryUsageCheck{WarnGe: 85, CriticalGe: 90},
		// SwapUsage 不配置阈值
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
		if event.Labels["check"] == "mem::swap_usage" {
			t.Error("swap_usage should be skipped when thresholds are 0")
		}
	}
}
