package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNetDevLine(t *testing.T) {
	tests := []struct {
		line    string
		wantOK  bool
		name    string
		rxBytes uint64
		txDrops uint64
	}{
		{
			"  eth0: 1000 100 2 3 0 0 0 0 2000 200 1 0 0 0 0 0",
			true, "eth0", 1000, 0,
		},
		{
			"    lo: 500 50 0 0 0 0 0 0 500 50 0 0 0 0 0 0",
			true, "lo", 500, 0,
		},
		{
			"  bond0:12345678 9876543 0 5 0 0 0 0 87654321 7654321 0 3 0 0 0 0",
			true, "bond0", 12345678, 3,
		},
		{"no colon here", false, "", 0, 0},
		{"", false, "", 0, 0},
	}

	for _, tt := range tests {
		e, ok := parseNetDevLine(tt.line)
		if ok != tt.wantOK {
			t.Errorf("parseNetDevLine(%q): ok=%v, want %v", tt.line, ok, tt.wantOK)
			continue
		}
		if ok {
			if e.name != tt.name {
				t.Errorf("name=%q, want %q", e.name, tt.name)
			}
			if e.rxBytes != tt.rxBytes {
				t.Errorf("rxBytes=%d, want %d", e.rxBytes, tt.rxBytes)
			}
			if e.txDrops != tt.txDrops {
				t.Errorf("txDrops=%d, want %d", e.txDrops, tt.txDrops)
			}
		}
	}
}

func TestParseNetDev(t *testing.T) {
	content := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 100 0 0 0 0 0 0 1000 100 0 0 0 0 0 0
  eth0: 5000000 40000 0 5 0 0 0 0 3000000 30000 0 0 0 0 0 0
  eth1: 2000000 20000 3 0 0 0 0 0 1000000 10000 0 2 0 0 0 0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "net_dev")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := parseNetDev(path)
	if err != nil {
		t.Fatalf("parseNetDev: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestFormatNetIf(t *testing.T) {
	entries := []netIfEntry{
		{name: "eth0", rxBytes: 5000000, rxPackets: 40000, txBytes: 3000000, txPackets: 30000, rxDrops: 5},
		{name: "eth1", rxBytes: 2000000, rxPackets: 20000, txBytes: 1000000, txPackets: 10000, rxErrors: 3, txDrops: 2},
	}
	out := formatNetIf(entries)
	if !strings.Contains(out, "2 with errors/drops") {
		t.Fatal("expected error/drop count in header")
	}
	if !strings.Contains(out, "[!]") {
		t.Fatal("expected [!] marker")
	}
	if !strings.Contains(out, "eth0") {
		t.Fatal("expected eth0 in output")
	}
}

func TestHumanPkts(t *testing.T) {
	tests := []struct {
		n    uint64
		want string
	}{
		{500, "500"},
		{1500, "1.5K"},
		{2500000, "2.5M"},
		{3000000000, "3.0G"},
	}
	for _, tt := range tests {
		got := humanPkts(tt.n)
		if got != tt.want {
			t.Errorf("humanPkts(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
