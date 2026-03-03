package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDiskStatsLine(t *testing.T) {
	line := "   8       0 sda 50000 1000 2000000 30000 40000 500 1500000 20000 5 45000 50000"
	ds := parseDiskStatsLine(line)
	if ds == nil {
		t.Fatal("expected non-nil")
	}
	if ds.name != "sda" {
		t.Errorf("name=%q, want sda", ds.name)
	}
	if ds.readsCompleted != 50000 {
		t.Errorf("readsCompleted=%d, want 50000", ds.readsCompleted)
	}
	if ds.writesCompleted != 40000 {
		t.Errorf("writesCompleted=%d, want 40000", ds.writesCompleted)
	}
	if ds.ioTimeMs != 45000 {
		t.Errorf("ioTimeMs=%d, want 45000", ds.ioTimeMs)
	}
}

func TestParseDiskStatsLineSkipLoop(t *testing.T) {
	line := "   7       0 loop0 100 0 200 10 0 0 0 0 0 10 10"
	if parseDiskStatsLine(line) != nil {
		t.Error("loop devices should be skipped")
	}
}

func TestParseDiskStatsLineShort(t *testing.T) {
	if parseDiskStatsLine("too short") != nil {
		t.Error("short lines should return nil")
	}
}

func TestReadDiskStats(t *testing.T) {
	content := `   8       0 sda 100 10 2000 500 200 5 1500 300 2 700 800
   8       1 sda1 80 8 1600 400 150 3 1200 250 1 600 650
   7       0 loop0 0 0 0 0 0 0 0 0 0 0 0
 253       0 dm-0 50 0 1000 200 100 0 800 100 0 250 300
`
	dir := t.TempDir()
	path := filepath.Join(dir, "diskstats")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	stats, err := readDiskStats(path)
	if err != nil {
		t.Fatalf("readDiskStats: %v", err)
	}

	if _, ok := stats["loop0"]; ok {
		t.Error("loop0 should be filtered")
	}
	if _, ok := stats["sda"]; !ok {
		t.Error("sda should be present")
	}
	if _, ok := stats["dm-0"]; !ok {
		t.Error("dm-0 should be present")
	}
}

func TestComputeDiskDeltas(t *testing.T) {
	snap1 := map[string]*diskStats{
		"sda": {name: "sda", readsCompleted: 1000, writesCompleted: 2000,
			sectorsRead: 100000, sectorsWritten: 200000,
			readTimeMs: 5000, writeTimeMs: 10000, ioTimeMs: 8000},
	}
	snap2 := map[string]*diskStats{
		"sda": {name: "sda", readsCompleted: 1100, writesCompleted: 2200,
			sectorsRead: 110000, sectorsWritten: 220000,
			readTimeMs: 6000, writeTimeMs: 12000, ioTimeMs: 9000, ioInProgress: 3},
	}

	deltas := computeDiskDeltas(snap1, snap2, time.Second)
	if len(deltas) != 1 {
		t.Fatalf("expected 1 delta, got %d", len(deltas))
	}

	d := deltas[0]
	if d.readIOPS != 100 {
		t.Errorf("readIOPS=%f, want 100", d.readIOPS)
	}
	if d.writeIOPS != 200 {
		t.Errorf("writeIOPS=%f, want 200", d.writeIOPS)
	}
	// await = (1000 + 2000) / (100 + 200) = 10ms
	if d.awaitMs != 10 {
		t.Errorf("awaitMs=%f, want 10", d.awaitMs)
	}
	if d.ioInProgress != 3 {
		t.Errorf("ioInProgress=%d, want 3", d.ioInProgress)
	}
}

func TestFormatDiskLatencyIdle(t *testing.T) {
	out := formatDiskLatency(nil, time.Second, false)
	if !strings.Contains(out, "idle") {
		t.Error("expected idle message")
	}
}

func TestFormatDiskLatencyHighAwait(t *testing.T) {
	deltas := []diskDelta{
		{name: "sda", readIOPS: 50, writeIOPS: 100, awaitMs: 150, util: 98},
	}
	out := formatDiskLatency(deltas, time.Second, false)
	if !strings.Contains(out, "[!!!]") {
		t.Error("expected [!!!] for high await")
	}
	if !strings.Contains(out, "SATURATED") {
		t.Error("expected SATURATED for high util")
	}
}
