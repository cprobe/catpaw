package filefd

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
		t.Skip("filefd only supports linux")
	}

	tests := []struct {
		name    string
		ins     Instance
		wantErr bool
	}{
		{
			name: "valid thresholds",
			ins:  Instance{FilefdUsage: FilefdUsageCheck{WarnGe: 80, CriticalGe: 90}},
		},
		{
			name: "warn only",
			ins:  Instance{FilefdUsage: FilefdUsageCheck{WarnGe: 80}},
		},
		{
			name: "critical only",
			ins:  Instance{FilefdUsage: FilefdUsageCheck{CriticalGe: 90}},
		},
		{
			name: "no thresholds - silent skip",
			ins:  Instance{},
		},
		{
			name:    "warn_ge >= critical_ge",
			ins:     Instance{FilefdUsage: FilefdUsageCheck{WarnGe: 90, CriticalGe: 80}},
			wantErr: true,
		},
		{
			name:    "warn_ge == critical_ge",
			ins:     Instance{FilefdUsage: FilefdUsageCheck{WarnGe: 80, CriticalGe: 80}},
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
		t.Skip("filefd only supports linux")
	}

	tests := []struct {
		name    string
		ins     Instance
		wantErr bool
	}{
		{
			name:    "warn_ge over 100",
			ins:     Instance{FilefdUsage: FilefdUsageCheck{WarnGe: 150}},
			wantErr: true,
		},
		{
			name:    "critical_ge over 100",
			ins:     Instance{FilefdUsage: FilefdUsageCheck{CriticalGe: 101}},
			wantErr: true,
		},
		{
			name:    "warn_ge negative",
			ins:     Instance{FilefdUsage: FilefdUsageCheck{WarnGe: -1}},
			wantErr: true,
		},
		{
			name:    "critical_ge negative",
			ins:     Instance{FilefdUsage: FilefdUsageCheck{CriticalGe: -5}},
			wantErr: true,
		},
		{
			name: "both at boundary - valid",
			ins:  Instance{FilefdUsage: FilefdUsageCheck{WarnGe: 80, CriticalGe: 100}},
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
		t.Skip("filefd only supports linux")
	}

	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	ins.Gather(q)

	if q.Len() != 0 {
		t.Errorf("expected 0 events when unconfigured, got %d", q.Len())
	}
}

func TestReadFileNr_ValidData(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filefd only supports linux")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "file-nr")
	os.WriteFile(path, []byte("9344\t0\t393164\n"), 0644)

	origPath := fileNrPath
	defer func() { fileNrPath = origPath }()
	fileNrPath = path

	allocated, max, err := readFileNr()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allocated != 9344 {
		t.Errorf("expected allocated 9344, got %d", allocated)
	}
	if max != 393164 {
		t.Errorf("expected max 393164, got %d", max)
	}
}

func TestReadFileNr_SpaceSeparated(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filefd only supports linux")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "file-nr")
	os.WriteFile(path, []byte("1234  0  500000\n"), 0644)

	origPath := fileNrPath
	defer func() { fileNrPath = origPath }()
	fileNrPath = path

	allocated, max, err := readFileNr()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allocated != 1234 {
		t.Errorf("expected allocated 1234, got %d", allocated)
	}
	if max != 500000 {
		t.Errorf("expected max 500000, got %d", max)
	}
}

func TestReadFileNr_ParseError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filefd only supports linux")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "file-nr")
	os.WriteFile(path, []byte("not_a_number\t0\t393164\n"), 0644)

	origPath := fileNrPath
	defer func() { fileNrPath = origPath }()
	fileNrPath = path

	_, _, err := readFileNr()
	if err == nil {
		t.Error("expected parse error")
	}
}

func TestReadFileNr_BadFormat(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filefd only supports linux")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "file-nr")
	os.WriteFile(path, []byte("9344\n"), 0644)

	origPath := fileNrPath
	defer func() { fileNrPath = origPath }()
	fileNrPath = path

	_, _, err := readFileNr()
	if err == nil {
		t.Error("expected format error for insufficient fields")
	}
}

func TestReadFileNr_FileNotFound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filefd only supports linux")
	}

	origPath := fileNrPath
	defer func() { fileNrPath = origPath }()
	fileNrPath = "/nonexistent/file-nr"

	_, _, err := readFileNr()
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestGather_WithMockedFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filefd only supports linux")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "file-nr")

	origPath := fileNrPath
	defer func() { fileNrPath = origPath }()
	fileNrPath = path

	tests := []struct {
		name           string
		content        string
		warnGe         float64
		criticalGe     float64
		expectedStatus string
	}{
		{
			name: "ok", content: "9344\t0\t393164\n",
			warnGe: 80, criticalGe: 90,
			expectedStatus: types.EventStatusOk,
		},
		{
			name: "warning", content: "320000\t0\t393164\n",
			warnGe: 80, criticalGe: 90,
			expectedStatus: types.EventStatusWarning,
		},
		{
			name: "critical", content: "362945\t0\t393164\n",
			warnGe: 80, criticalGe: 90,
			expectedStatus: types.EventStatusCritical,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.WriteFile(path, []byte(tt.content), 0644)

			ins := &Instance{
				FilefdUsage: FilefdUsageCheck{WarnGe: tt.warnGe, CriticalGe: tt.criticalGe},
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
			if event.Labels["check"] != "filefd::filefd_usage" {
				t.Errorf("unexpected check label: %s", event.Labels["check"])
			}
			if event.Labels["target"] != "system" {
				t.Errorf("unexpected target label: %s", event.Labels["target"])
			}
			if event.Labels[types.AttrPrefix+"allocated"] == "" {
				t.Error("missing _attr_allocated")
			}
			if event.Labels[types.AttrPrefix+"max"] == "" {
				t.Error("missing _attr_max")
			}
			if event.Labels[types.AttrPrefix+"usage_percent"] == "" {
				t.Error("missing _attr_usage_percent")
			}
		})
	}
}

func TestGather_MaxZero(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filefd only supports linux")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "file-nr")
	os.WriteFile(path, []byte("100\t0\t0\n"), 0644)

	origPath := fileNrPath
	defer func() { fileNrPath = origPath }()
	fileNrPath = path

	ins := &Instance{
		FilefdUsage: FilefdUsageCheck{WarnGe: 80, CriticalGe: 90},
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

func TestGather_ReadError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("filefd only supports linux")
	}

	origPath := fileNrPath
	defer func() { fileNrPath = origPath }()
	fileNrPath = "/nonexistent/file-nr"

	ins := &Instance{
		FilefdUsage: FilefdUsageCheck{WarnGe: 80, CriticalGe: 90},
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
