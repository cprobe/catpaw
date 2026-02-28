package conntrack

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
		t.Skip("conntrack only supports linux")
	}

	tests := []struct {
		name    string
		ins     Instance
		wantErr bool
	}{
		{
			name: "valid thresholds",
			ins:  Instance{ConntrackUsage: ConntrackUsageCheck{WarnGe: 75, CriticalGe: 90}},
		},
		{
			name: "warn only",
			ins:  Instance{ConntrackUsage: ConntrackUsageCheck{WarnGe: 80}},
		},
		{
			name: "critical only",
			ins:  Instance{ConntrackUsage: ConntrackUsageCheck{CriticalGe: 90}},
		},
		{
			name: "no thresholds - silent skip",
			ins:  Instance{},
		},
		{
			name:    "warn_ge >= critical_ge",
			ins:     Instance{ConntrackUsage: ConntrackUsageCheck{WarnGe: 90, CriticalGe: 80}},
			wantErr: true,
		},
		{
			name:    "warn_ge == critical_ge",
			ins:     Instance{ConntrackUsage: ConntrackUsageCheck{WarnGe: 80, CriticalGe: 80}},
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
		t.Skip("conntrack only supports linux")
	}

	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 0 {
		t.Errorf("expected 0 events when unconfigured, got %d", q.Len())
	}
}

func TestReadConntrackFiles_ModuleNotLoaded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}

	origPaths := conntrackPaths
	defer func() { conntrackPaths = origPaths }()

	conntrackPaths = [][2]string{
		{"/nonexistent/path1/count", "/nonexistent/path1/max"},
		{"/nonexistent/path2/count", "/nonexistent/path2/max"},
	}

	_, _, err := readConntrackFiles()
	if err != errModuleNotLoaded {
		t.Errorf("expected errModuleNotLoaded, got: %v", err)
	}
}

func TestReadConntrackFiles_ValidData(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}

	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	maxPath := filepath.Join(dir, "max")

	os.WriteFile(countPath, []byte("12345\n"), 0644)
	os.WriteFile(maxPath, []byte("262144\n"), 0644)

	origPaths := conntrackPaths
	defer func() { conntrackPaths = origPaths }()
	conntrackPaths = [][2]string{{countPath, maxPath}}

	count, max, err := readConntrackFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 12345 {
		t.Errorf("expected count 12345, got %d", count)
	}
	if max != 262144 {
		t.Errorf("expected max 262144, got %d", max)
	}
}

func TestReadConntrackFiles_ParseError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}

	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	maxPath := filepath.Join(dir, "max")

	os.WriteFile(countPath, []byte("not_a_number\n"), 0644)
	os.WriteFile(maxPath, []byte("262144\n"), 0644)

	origPaths := conntrackPaths
	defer func() { conntrackPaths = origPaths }()
	conntrackPaths = [][2]string{{countPath, maxPath}}

	_, _, err := readConntrackFiles()
	if err == nil {
		t.Error("expected parse error")
	}
}

func TestReadConntrackFiles_Fallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}

	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	maxPath := filepath.Join(dir, "max")

	os.WriteFile(countPath, []byte("500\n"), 0644)
	os.WriteFile(maxPath, []byte("1000\n"), 0644)

	origPaths := conntrackPaths
	defer func() { conntrackPaths = origPaths }()
	conntrackPaths = [][2]string{
		{"/nonexistent/primary/count", "/nonexistent/primary/max"},
		{countPath, maxPath},
	}

	count, max, err := readConntrackFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 500 || max != 1000 {
		t.Errorf("expected 500/1000, got %d/%d", count, max)
	}
}

func TestGather_WithMockedFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}

	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	maxPath := filepath.Join(dir, "max")

	origPaths := conntrackPaths
	defer func() { conntrackPaths = origPaths }()
	conntrackPaths = [][2]string{{countPath, maxPath}}

	tests := []struct {
		name           string
		count          string
		max            string
		warnGe         float64
		criticalGe     float64
		expectedStatus string
	}{
		{
			name: "ok", count: "1000", max: "262144",
			warnGe: 75, criticalGe: 90,
			expectedStatus: types.EventStatusOk,
		},
		{
			name: "warning", count: "200000", max: "262144",
			warnGe: 75, criticalGe: 90,
			expectedStatus: types.EventStatusWarning,
		},
		{
			name: "critical", count: "240000", max: "262144",
			warnGe: 75, criticalGe: 90,
			expectedStatus: types.EventStatusCritical,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.WriteFile(countPath, []byte(tt.count+"\n"), 0644)
			os.WriteFile(maxPath, []byte(tt.max+"\n"), 0644)

			ins := &Instance{
				ConntrackUsage: ConntrackUsageCheck{WarnGe: tt.warnGe, CriticalGe: tt.criticalGe},
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
			if event.Labels["check"] != "conntrack::conntrack_usage" {
				t.Errorf("unexpected check label: %s", event.Labels["check"])
			}
			if event.Labels["target"] != "system" {
				t.Errorf("unexpected target label: %s", event.Labels["target"])
			}
			if event.Labels[types.AttrPrefix+"count"] == "" {
				t.Error("missing _attr_count")
			}
			if event.Labels[types.AttrPrefix+"max"] == "" {
				t.Error("missing _attr_max")
			}
		})
	}
}

func TestGather_MaxZero(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}

	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	maxPath := filepath.Join(dir, "max")

	os.WriteFile(countPath, []byte("100\n"), 0644)
	os.WriteFile(maxPath, []byte("0\n"), 0644)

	origPaths := conntrackPaths
	defer func() { conntrackPaths = origPaths }()
	conntrackPaths = [][2]string{{countPath, maxPath}}

	ins := &Instance{
		ConntrackUsage: ConntrackUsageCheck{WarnGe: 75, CriticalGe: 90},
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}

	all := q.PopBackAll()
	event := all[0]
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("expected Critical for max=0, got %s: %s", event.EventStatus, event.Description)
	}
}

func TestGather_ModuleNotLoaded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}

	origPaths := conntrackPaths
	defer func() { conntrackPaths = origPaths }()
	conntrackPaths = [][2]string{
		{"/nonexistent/path/count", "/nonexistent/path/max"},
	}

	ins := &Instance{
		ConntrackUsage: ConntrackUsageCheck{WarnGe: 75, CriticalGe: 90},
	}

	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 0 {
		t.Errorf("expected 0 events when module not loaded, got %d", q.Len())
	}
}

func TestGather_ReadError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}
	if os.Getuid() == 0 {
		t.Skip("test requires non-root to trigger permission denied")
	}

	dir := t.TempDir()
	countPath := filepath.Join(dir, "count")
	maxPath := filepath.Join(dir, "max")

	os.WriteFile(countPath, []byte("12345\n"), 0000)
	os.WriteFile(maxPath, []byte("262144\n"), 0644)

	origPaths := conntrackPaths
	defer func() { conntrackPaths = origPaths }()
	conntrackPaths = [][2]string{{countPath, maxPath}}

	ins := &Instance{
		ConntrackUsage: ConntrackUsageCheck{WarnGe: 75, CriticalGe: 90},
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

func TestInit_ThresholdUpperBound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("conntrack only supports linux")
	}

	tests := []struct {
		name    string
		ins     Instance
		wantErr bool
	}{
		{
			name: "warn_ge over 100",
			ins:  Instance{ConntrackUsage: ConntrackUsageCheck{WarnGe: 150}},
			wantErr: true,
		},
		{
			name: "critical_ge over 100",
			ins:  Instance{ConntrackUsage: ConntrackUsageCheck{CriticalGe: 101}},
			wantErr: true,
		},
		{
			name: "both at 100 - valid",
			ins:  Instance{ConntrackUsage: ConntrackUsageCheck{WarnGe: 80, CriticalGe: 100}},
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
