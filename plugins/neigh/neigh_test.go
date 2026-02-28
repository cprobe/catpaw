package neigh

import (
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
		t.Skip("neigh only supports linux")
	}

	tests := []struct {
		name    string
		ins     Instance
		wantErr bool
	}{
		{
			name: "valid thresholds",
			ins:  Instance{NeighUsage: NeighUsageCheck{WarnGe: 75, CriticalGe: 90}},
		},
		{
			name: "warn only",
			ins:  Instance{NeighUsage: NeighUsageCheck{WarnGe: 75}},
		},
		{
			name: "critical only",
			ins:  Instance{NeighUsage: NeighUsageCheck{CriticalGe: 90}},
		},
		{
			name: "no thresholds - silent skip",
			ins:  Instance{},
		},
		{
			name:    "warn_ge >= critical_ge",
			ins:     Instance{NeighUsage: NeighUsageCheck{WarnGe: 90, CriticalGe: 75}},
			wantErr: true,
		},
		{
			name:    "warn_ge == critical_ge",
			ins:     Instance{NeighUsage: NeighUsageCheck{WarnGe: 80, CriticalGe: 80}},
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

func TestInit_ThresholdBounds(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	tests := []struct {
		name    string
		ins     Instance
		wantErr bool
	}{
		{
			name:    "warn_ge over 100",
			ins:     Instance{NeighUsage: NeighUsageCheck{WarnGe: 150}},
			wantErr: true,
		},
		{
			name:    "critical_ge over 100",
			ins:     Instance{NeighUsage: NeighUsageCheck{CriticalGe: 101}},
			wantErr: true,
		},
		{
			name:    "warn_ge negative",
			ins:     Instance{NeighUsage: NeighUsageCheck{WarnGe: -1}},
			wantErr: true,
		},
		{
			name:    "critical_ge negative",
			ins:     Instance{NeighUsage: NeighUsageCheck{CriticalGe: -5}},
			wantErr: true,
		},
		{
			name: "both at boundary - valid",
			ins:  Instance{NeighUsage: NeighUsageCheck{WarnGe: 75, CriticalGe: 100}},
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
		t.Skip("neigh only supports linux")
	}

	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 0 {
		t.Errorf("expected 0 events when unconfigured, got %d", q.Len())
	}
}

func setupMockFiles(t *testing.T, thresh3Content, arpContent string) (cleanup func()) {
	t.Helper()
	dir := t.TempDir()

	thresh3File := filepath.Join(dir, "gc_thresh3")
	arpFile := filepath.Join(dir, "arp")

	os.WriteFile(thresh3File, []byte(thresh3Content), 0644)
	os.WriteFile(arpFile, []byte(arpContent), 0644)

	origArpPath := arpPath
	origThresh3Path := gcThresh3Path
	arpPath = arpFile
	gcThresh3Path = thresh3File

	return func() {
		arpPath = origArpPath
		gcThresh3Path = origThresh3Path
	}
}

const arpHeader = "IP address       HW type     Flags       HW address            Mask     Device\n"

func TestReadNeighData_ValidData(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	arpContent := arpHeader +
		"192.168.1.1      0x1         0x2         00:11:22:33:44:55     *        eth0\n" +
		"10.244.0.3       0x1         0x2         aa:bb:cc:dd:ee:ff     *        cni0\n" +
		"10.244.0.5       0x1         0x2         11:22:33:44:55:66     *        cni0\n"

	cleanup := setupMockFiles(t, "1024\n", arpContent)
	defer cleanup()

	entries, gcThresh3, err := readNeighData()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != 3 {
		t.Errorf("expected 3 entries, got %d", entries)
	}
	if gcThresh3 != 1024 {
		t.Errorf("expected gc_thresh3 1024, got %d", gcThresh3)
	}
}

func TestReadNeighData_EmptyArpTable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	cleanup := setupMockFiles(t, "1024\n", arpHeader)
	defer cleanup()

	entries, gcThresh3, err := readNeighData()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != 0 {
		t.Errorf("expected 0 entries for header-only arp, got %d", entries)
	}
	if gcThresh3 != 1024 {
		t.Errorf("expected gc_thresh3 1024, got %d", gcThresh3)
	}
}

func TestReadNeighData_EmptyFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	cleanup := setupMockFiles(t, "1024\n", "")
	defer cleanup()

	entries, gcThresh3, err := readNeighData()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != 0 {
		t.Errorf("expected 0 entries for empty file, got %d", entries)
	}
	if gcThresh3 != 1024 {
		t.Errorf("expected gc_thresh3 1024, got %d", gcThresh3)
	}
}

func TestReadNeighData_Thresh3ParseError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	cleanup := setupMockFiles(t, "not_a_number\n", arpHeader)
	defer cleanup()

	_, _, err := readNeighData()
	if err == nil {
		t.Error("expected parse error for gc_thresh3")
	}
}

func TestReadNeighData_Thresh3FileNotFound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	origThresh3Path := gcThresh3Path
	defer func() { gcThresh3Path = origThresh3Path }()
	gcThresh3Path = "/nonexistent/gc_thresh3"

	_, _, err := readNeighData()
	if err == nil {
		t.Error("expected error for missing gc_thresh3 file")
	}
}

func TestReadNeighData_ArpFileNotFound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	dir := t.TempDir()
	thresh3File := filepath.Join(dir, "gc_thresh3")
	os.WriteFile(thresh3File, []byte("1024\n"), 0644)

	origArpPath := arpPath
	origThresh3Path := gcThresh3Path
	defer func() {
		arpPath = origArpPath
		gcThresh3Path = origThresh3Path
	}()
	gcThresh3Path = thresh3File
	arpPath = "/nonexistent/arp"

	_, _, err := readNeighData()
	if err == nil {
		t.Error("expected error for missing arp file")
	}
}

func TestGather_WithMockedFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	arpOk := arpHeader +
		"192.168.1.1      0x1         0x2         00:11:22:33:44:55     *        eth0\n"

	arpWarning := arpHeader
	for i := 0; i < 800; i++ {
		arpWarning += "10.0.0.1         0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0\n"
	}

	arpCritical := arpHeader
	for i := 0; i < 946; i++ {
		arpCritical += "10.0.0.1         0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0\n"
	}

	tests := []struct {
		name           string
		thresh3        string
		arpContent     string
		warnGe         float64
		criticalGe     float64
		expectedStatus string
	}{
		{
			name: "ok", thresh3: "1024", arpContent: arpOk,
			warnGe: 75, criticalGe: 90,
			expectedStatus: types.EventStatusOk,
		},
		{
			name: "warning", thresh3: "1024", arpContent: arpWarning,
			warnGe: 75, criticalGe: 90,
			expectedStatus: types.EventStatusWarning,
		},
		{
			name: "critical", thresh3: "1024", arpContent: arpCritical,
			warnGe: 75, criticalGe: 90,
			expectedStatus: types.EventStatusCritical,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := setupMockFiles(t, tt.thresh3+"\n", tt.arpContent)
			defer cleanup()

			ins := &Instance{
				NeighUsage: NeighUsageCheck{WarnGe: tt.warnGe, CriticalGe: tt.criticalGe},
			}

			q := safe.NewQueue[*types.Event]()
			ins.Gather(q)

			if q.Len() != 1 {
				t.Fatalf("expected 1 event, got %d", q.Len())
			}

			all := q.PopBackAll()
			event := all[0]
			if event.EventStatus != tt.expectedStatus {
				t.Errorf("expected %s, got %s: %s", tt.expectedStatus, event.EventStatus, event.Description)
			}
			if event.Labels["check"] != "neigh::neigh_usage" {
				t.Errorf("unexpected check label: %s", event.Labels["check"])
			}
			if event.Labels["target"] != "system" {
				t.Errorf("unexpected target label: %s", event.Labels["target"])
			}
			if event.Labels[types.AttrPrefix+"entries"] == "" {
				t.Error("missing _attr_entries")
			}
			if event.Labels[types.AttrPrefix+"gc_thresh3"] == "" {
				t.Error("missing _attr_gc_thresh3")
			}
			if event.Labels[types.AttrPrefix+"usage_percent"] == "" {
				t.Error("missing _attr_usage_percent")
			}
		})
	}
}

func TestGather_Thresh3Zero(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	cleanup := setupMockFiles(t, "0\n", arpHeader+"192.168.1.1      0x1         0x2         00:11:22:33:44:55     *        eth0\n")
	defer cleanup()

	ins := &Instance{
		NeighUsage: NeighUsageCheck{WarnGe: 75, CriticalGe: 90},
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	all := q.PopBackAll()
	event := all[0]
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for gc_thresh3=0, got %s: %s", event.EventStatus, event.Description)
	}
}

func TestGather_ReadError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("neigh only supports linux")
	}

	origThresh3Path := gcThresh3Path
	defer func() { gcThresh3Path = origThresh3Path }()
	gcThresh3Path = "/nonexistent/gc_thresh3"

	ins := &Instance{
		NeighUsage: NeighUsageCheck{WarnGe: 75, CriticalGe: 90},
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
