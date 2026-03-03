package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSoftnetStat(t *testing.T) {
	content := `0073d1a2 00000000 00000003 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000
004f8c1b 00000005 00000001 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000
002a4e10 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000
`
	dir := t.TempDir()
	path := filepath.Join(dir, "softnet_stat")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cpus, err := parseSoftnetStat(path)
	if err != nil {
		t.Fatalf("parseSoftnetStat: %v", err)
	}
	if len(cpus) != 3 {
		t.Fatalf("expected 3 CPUs, got %d", len(cpus))
	}

	if cpus[0].processed != 0x0073d1a2 {
		t.Errorf("cpu0.processed=%d, want %d", cpus[0].processed, 0x0073d1a2)
	}
	if cpus[0].dropped != 0 {
		t.Errorf("cpu0.dropped=%d, want 0", cpus[0].dropped)
	}
	if cpus[0].timeSqueeze != 3 {
		t.Errorf("cpu0.timeSqueeze=%d, want 3", cpus[0].timeSqueeze)
	}

	if cpus[1].dropped != 5 {
		t.Errorf("cpu1.dropped=%d, want 5", cpus[1].dropped)
	}
	if cpus[1].timeSqueeze != 1 {
		t.Errorf("cpu1.timeSqueeze=%d, want 1", cpus[1].timeSqueeze)
	}
}

func TestFormatSoftnet(t *testing.T) {
	cpus := []softnetCPU{
		{cpu: 0, processed: 1000000, dropped: 0, timeSqueeze: 3},
		{cpu: 1, processed: 800000, dropped: 5, timeSqueeze: 1},
		{cpu: 2, processed: 600000, dropped: 0, timeSqueeze: 0},
	}
	out := formatSoftnet(cpus)

	if !strings.Contains(out, "3 CPUs") {
		t.Error("expected CPU count")
	}
	if !strings.Contains(out, "[!drop]") {
		t.Error("expected drop flag on CPU 1")
	}
	if !strings.Contains(out, "[!squeeze]") {
		t.Error("expected squeeze flag")
	}
	if !strings.Contains(out, "netdev_max_backlog") {
		t.Error("expected backlog recommendation")
	}
	if !strings.Contains(out, "netdev_budget") {
		t.Error("expected budget recommendation")
	}
}

func TestFormatSoftnetHealthy(t *testing.T) {
	cpus := []softnetCPU{
		{cpu: 0, processed: 1000000, dropped: 0, timeSqueeze: 0},
	}
	out := formatSoftnet(cpus)
	if !strings.Contains(out, "healthy") {
		t.Error("expected healthy message")
	}
}

func TestFormatSoftnetEmpty(t *testing.T) {
	out := formatSoftnet(nil)
	if !strings.Contains(out, "No softnet") {
		t.Error("expected no data message")
	}
}
