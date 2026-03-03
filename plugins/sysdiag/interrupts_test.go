package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseIRQLine(t *testing.T) {
	tests := []struct {
		line    string
		numCPUs int
		wantOK  bool
		name    string
		total   uint64
	}{
		{"  0:  100  200  300  IR-IO-APIC 2-edge timer", 3, true, "0", 600},
		{" LOC:  50000  60000  Local timer interrupts", 2, true, "LOC", 110000},
		{"bad line without colon", 2, false, "", 0},
		{"ERR:  abc  def", 2, false, "", 0},
	}
	for _, tt := range tests {
		e, ok := parseIRQLine(tt.line, tt.numCPUs)
		if ok != tt.wantOK {
			t.Errorf("parseIRQLine(%q): ok=%v, want %v", tt.line, ok, tt.wantOK)
			continue
		}
		if ok {
			if e.name != tt.name {
				t.Errorf("parseIRQLine(%q): name=%q, want %q", tt.line, e.name, tt.name)
			}
			if e.total != tt.total {
				t.Errorf("parseIRQLine(%q): total=%d, want %d", tt.line, e.total, tt.total)
			}
		}
	}
}

func TestParseInterrupts(t *testing.T) {
	content := `           CPU0       CPU1       CPU2
  0:         10         20         30   IR-IO-APIC   2-edge      timer
  1:          0          0          0   IR-IO-APIC   1-edge      i8042
LOC:     500000     600000     100000   Local timer interrupts
`
	dir := t.TempDir()
	path := filepath.Join(dir, "interrupts")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	entries, numCPUs, err := parseInterrupts(path)
	if err != nil {
		t.Fatalf("parseInterrupts: %v", err)
	}
	if numCPUs != 3 {
		t.Fatalf("expected 3 CPUs, got %d", numCPUs)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// LOC should have the highest total
	found := false
	for _, e := range entries {
		if e.name == "LOC" {
			found = true
			if e.total != 1200000 {
				t.Errorf("LOC total=%d, want 1200000", e.total)
			}
			if e.maxCPU != 1 {
				t.Errorf("LOC maxCPU=%d, want 1", e.maxCPU)
			}
		}
	}
	if !found {
		t.Fatal("LOC entry not found")
	}
}

func TestFormatInterrupts(t *testing.T) {
	entries := []irqEntry{
		{name: "LOC", desc: "Local timer", total: 1200000, numCPUs: 3, maxCPU: 1, maxCount: 600000, minCount: 100000, perCPU: []uint64{500000, 600000, 100000}},
		{name: "0", desc: "timer", total: 60, numCPUs: 3, maxCPU: 2, maxCount: 30, minCount: 10, perCPU: []uint64{10, 20, 30}},
	}
	out := formatInterrupts(entries, 3, 10)
	if !strings.Contains(out, "LOC") {
		t.Fatal("expected LOC in output")
	}
	if !strings.Contains(out, "3 CPUs") {
		t.Fatal("expected CPU count in header")
	}
}

func TestFormatInterruptsImbalance(t *testing.T) {
	entries := []irqEntry{
		{name: "eth0", desc: "eth0-rx-0", total: 1000, numCPUs: 4, maxCPU: 0, maxCount: 900, minCount: 10, perCPU: []uint64{900, 30, 60, 10}},
	}
	out := formatInterrupts(entries, 4, 10)
	if !strings.Contains(out, "CPU0") {
		t.Fatal("expected CPU0 as hot CPU for imbalanced IRQ")
	}
	if !strings.Contains(out, "x") {
		t.Fatal("expected imbalance ratio in output")
	}
}

func TestExecInterrupts_Validation(t *testing.T) {
	_, err := execInterrupts(t.Context(), map[string]string{"top": "abc"})
	if err == nil || !strings.Contains(err.Error(), "invalid top") {
		t.Fatalf("expected 'invalid top' error, got: %v", err)
	}
}
