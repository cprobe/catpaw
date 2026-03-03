package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseARPTable(t *testing.T) {
	content := `IP address       HW type     Flags       HW address            Mask     Device
10.0.0.1         0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
10.0.0.2         0x1         0x2         11:22:33:44:55:66     *        eth0
10.0.0.3         0x1         0x0         00:00:00:00:00:00     *        eth0
172.17.0.2       0x1         0x2         02:42:ac:11:00:02     *        docker0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "arp")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	entries, truncated, err := parseARPTable(path)
	if err != nil {
		t.Fatalf("parseARPTable: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	if entries[2].ip != "10.0.0.3" {
		t.Errorf("entry[2].ip=%q, want 10.0.0.3", entries[2].ip)
	}
	if !entries[2].isIncomplete() {
		t.Fatal("entry[2] should be incomplete (flags=0x0)")
	}
	if entries[0].isIncomplete() {
		t.Fatal("entry[0] should not be incomplete")
	}
}

func TestFormatARP(t *testing.T) {
	entries := []arpEntry{
		{ip: "10.0.0.1", hwAddr: "aa:bb:cc:dd:ee:ff", flags: "0x2", device: "eth0"},
		{ip: "10.0.0.2", hwAddr: "00:00:00:00:00:00", flags: "0x0", device: "eth0"},
		{ip: "172.17.0.2", hwAddr: "02:42:ac:11:00:02", flags: "0x2", device: "docker0"},
	}

	out := formatARP(entries, 1024, false, false)
	if !strings.Contains(out, "3 entries") {
		t.Fatal("expected entry count")
	}
	if !strings.Contains(out, "gc_thresh3=1024") {
		t.Fatal("expected gc_thresh3 in output")
	}
	if !strings.Contains(out, "1 incomplete") {
		t.Fatal("expected incomplete count")
	}
	if !strings.Contains(out, "eth0") {
		t.Fatal("expected eth0 in per-device breakdown")
	}
}

func TestFormatARPHighUsage(t *testing.T) {
	entries := make([]arpEntry, 950)
	for i := range entries {
		entries[i] = arpEntry{ip: "10.0.0.1", hwAddr: "aa:bb:cc:dd:ee:ff", flags: "0x2", device: "eth0"}
	}
	out := formatARP(entries, 1024, false, false)
	if !strings.Contains(out, "[!!!]") {
		t.Fatalf("expected [!!!] for >90%% usage, got: %s", out)
	}
}

func TestFormatARPShowAll(t *testing.T) {
	entries := []arpEntry{
		{ip: "10.0.0.1", hwAddr: "aa:bb:cc:dd:ee:ff", flags: "0x2", device: "eth0"},
	}
	out := formatARP(entries, 0, true, false)
	if !strings.Contains(out, "All entries") {
		t.Fatal("expected 'All entries' section when show_all=true")
	}
}
